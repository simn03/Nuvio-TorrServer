package rank

import (
	"testing"

	"family-torrserver/internal/config"
	"family-torrserver/internal/prowlarr"
)

func TestParseSampleTitles(t *testing.T) {
	cases := []struct {
		title      string
		res        string
		hevc       bool
		hasSeason  bool
		season     int
		hasEpisode bool
		episode    int
	}{
		{"The Matrix 1999 1080p BluRay x264-GROUP", "1080p", false, false, 0, false, 0},
		{"Some.Show.S01E05.1080p.WEB-DL.x265-HEVC", "1080p", true, true, 1, true, 5},
		{"Some Show 1x05 720p HDTV", "720p", false, true, 1, true, 5},
		{"Some.Show.S01.1080p.BluRay.x264", "1080p", false, true, 1, false, 0}, // season pack
		{"Movie.2021.2160p.UHD.BluRay.x265", "4k", true, false, 0, false, 0},   // 2160p -> 4k
		{"Show Season 2 Complete 480p", "480p", false, true, 2, false, 0},
		{"Some.Show.S03 E07.720p.HEVC", "720p", true, true, 3, true, 7}, // spaced S/E
	}
	for _, c := range cases {
		p := Parse(c.title)
		if p.Resolution != c.res {
			t.Errorf("%q: resolution = %q, want %q", c.title, p.Resolution, c.res)
		}
		if p.IsHEVC != c.hevc {
			t.Errorf("%q: IsHEVC = %v, want %v", c.title, p.IsHEVC, c.hevc)
		}
		if p.HasSeason != c.hasSeason || p.Season != c.season {
			t.Errorf("%q: season = (%v,%d), want (%v,%d)", c.title, p.HasSeason, p.Season, c.hasSeason, c.season)
		}
		if p.HasEpisode != c.hasEpisode || p.Episode != c.episode {
			t.Errorf("%q: episode = (%v,%d), want (%v,%d)", c.title, p.HasEpisode, p.Episode, c.hasEpisode, c.episode)
		}
	}
}

func mk(title string, seeders int, sizeGB float64) prowlarr.Result {
	return prowlarr.Result{
		Title:    title,
		Seeders:  seeders,
		Size:     int64(sizeGB * 1024 * 1024 * 1024),
		InfoHash: title, // unique enough for tests
	}
}

func TestRankFiltersAndSorts(t *testing.T) {
	results := []prowlarr.Result{
		mk("Movie 2020 2160p BluRay x265", 50, 30),   // 4k, hevc
		mk("Movie 2020 1080p BluRay x264", 200, 8),   // 1080p, high seeders
		mk("Movie 2020 720p WEB x265", 5, 2),         // 720p
		mk("Movie 2020 CAM XviD", 500, 1),            // cam -> excluded by quality
		mk("Movie 2020 480p DVDRip", 1, 1),           // below MinSeeders
		mk("Movie 2020 1080p BluRay REMUX", 100, 60), // over MaxSizeGB
	}

	cfg := config.UserConfig{
		Sort:             "quality",
		Resolutions:      []string{"4k", "1080p", "720p"},
		ExcludeQualities: []string{"cam"},
		PreferHEVC:       true,
		MaxSizeGB:        40,
		MinSeeders:       3,
		MaxResults:       5,
	}

	got := Rank(results, cfg)

	// cam (excluded), 480p (below seeders), 60GB (over cap) all dropped -> 3 left.
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3: %+v", len(got), titles(got))
	}
	// quality sort: 4k first, then 1080p, then 720p.
	wantOrder := []string{"4k", "1080p", "720p"}
	for i, w := range wantOrder {
		if got[i].Parsed.Resolution != w {
			t.Errorf("position %d resolution = %q, want %q (order: %v)", i, got[i].Parsed.Resolution, w, resList(got))
		}
	}
}

func TestRankSortSeedersAndTruncate(t *testing.T) {
	results := []prowlarr.Result{
		mk("A 1080p x264", 10, 5),
		mk("B 1080p x264", 300, 5),
		mk("C 1080p x264", 100, 5),
	}
	cfg := config.Default()
	cfg.Sort = "seeders"
	cfg.MaxResults = 2
	got := Rank(results, cfg)
	if len(got) != 2 {
		t.Fatalf("want 2 after truncate, got %d", len(got))
	}
	if got[0].Result.Seeders != 300 || got[1].Result.Seeders != 100 {
		t.Errorf("seeder order wrong: %d then %d", got[0].Result.Seeders, got[1].Result.Seeders)
	}
}

func titles(c []Candidate) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.Result.Title
	}
	return out
}

func resList(c []Candidate) []string {
	out := make([]string, len(c))
	for i, x := range c {
		out[i] = x.Parsed.Resolution
	}
	return out
}
