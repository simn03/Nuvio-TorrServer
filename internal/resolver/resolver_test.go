package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/cinemeta"
	"github.com/simn03/nuvio-p2p-http-addon/internal/config"
	"github.com/simn03/nuvio-p2p-http-addon/internal/prowlarr"
	"github.com/simn03/nuvio-p2p-http-addon/internal/rank"
	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

// fakeCache satisfies both cinemeta.Cache and prowlarr.Cache with no caching
// (always a miss) so tests exercise the live request path.
type fakeCache struct{}

func (fakeCache) CinemetaCacheGet(string, string) ([]byte, bool)               { return nil, false }
func (fakeCache) CinemetaCacheSet(string, string, []byte, time.Duration) error { return nil }
func (fakeCache) ProwlarrCacheGet(string) ([]byte, bool)                       { return nil, false }
func (fakeCache) ProwlarrCacheSet(string, []byte, time.Duration) error         { return nil }

func TestParseID(t *testing.T) {
	m, _ := ParseID("movie", "tt0133093")
	if m.Series || m.IMDb != "tt0133093" {
		t.Errorf("movie parse wrong: %+v", m)
	}
	m, _ = ParseID("series", "tt1234567:2:5")
	if !m.Series || m.Season != 2 || m.Episode != 5 {
		t.Errorf("series parse wrong: %+v", m)
	}
	m, _ = ParseID("series", "tt1234567%3A2%3A5")
	if m.IMDb != "tt1234567" || !m.Series || m.Season != 2 || m.Episode != 5 {
		t.Errorf("escaped series parse wrong: %+v", m)
	}
	if _, err := ParseID("movie", "nothing"); err == nil {
		t.Errorf("expected error for bad id")
	}
}

func TestResolveMovie(t *testing.T) {
	cine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/meta/movie/tt0133093.json" {
			t.Errorf("unexpected cinemeta path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"meta":{"name":"The Matrix","year":"1999"}}`))
	}))
	defer cine.Close()

	var queries []string
	var mu sync.Mutex
	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		mu.Unlock()
		writeReleases(w, []prowlarr.Result{
			{Title: "The Matrix 1999 2160p BluRay x265", Seeders: 40, Size: 25e9, InfoHash: "a"},
			{Title: "The Matrix 1999 1080p BluRay x264", Seeders: 300, Size: 8e9, InfoHash: "b"},
			{Title: "The Matrix 1999 CAM XviD", Seeders: 999, Size: 1e9, InfoHash: "c"},
		})
	}))
	defer prow.Close()

	r := New(
		cinemeta.New(cine.URL, fakeCache{}, time.Hour),
		prowlarr.New(prow.URL, "k", fakeCache{}, time.Hour, false),
		nil, nil, false,
	)

	cfg := config.Default() // excludes cam+ts, quality sort, min seeders 3
	got, _, err := r.Candidates(context.Background(), "movie", "tt0133093", cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 candidates (cam dropped), got %d", len(got))
	}
	if got[0].Parsed.Resolution != "4k" {
		t.Errorf("quality sort: first = %q, want 4k", got[0].Parsed.Resolution)
	}
	if len(queries) != 1 || queries[0] != "The Matrix 1999" {
		t.Errorf("movie query = %v, want [\"The Matrix 1999\"]", queries)
	}
}

func TestResolveSeriesIssuesEpisodeAndPackQueries(t *testing.T) {
	cine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"name":"Some Show","releaseInfo":"2015"}}`))
	}))
	defer cine.Close()

	var queries []string
	var mu sync.Mutex
	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		// Both queries return the same pack release to exercise dedupe.
		writeReleases(w, []prowlarr.Result{
			{Title: "Some Show S02 1080p WEB x265", Seeders: 50, Size: 20e9, InfoHash: "pack"},
		})
	}))
	defer prow.Close()

	r := New(
		cinemeta.New(cine.URL, fakeCache{}, time.Hour),
		prowlarr.New(prow.URL, "k", fakeCache{}, time.Hour, false),
		nil, nil, false,
	)

	got, _, err := r.Candidates(context.Background(), "series", "tt1234567:2:5", config.Default())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Same infohash from both queries => deduped to 1.
	if len(got) != 1 {
		t.Fatalf("want 1 deduped candidate, got %d", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 {
		t.Fatalf("want 2 queries (episode + pack), got %d: %v", len(queries), queries)
	}
	want := map[string]bool{"Some Show S02E05": false, "Some Show S02": false}
	for _, q := range queries {
		if _, ok := want[q]; !ok {
			t.Errorf("unexpected query %q", q)
		}
		want[q] = true
	}
	for q, seen := range want {
		if !seen {
			t.Errorf("missing expected query %q", q)
		}
	}
}

func TestSeriesFilterDropsOtherEpisodesButKeepsPacks(t *testing.T) {
	results := []prowlarr.Result{
		{Title: "House of the Dragon S01E01 1080p WEB-DL", InfoHash: "exact"},
		{Title: "House of the Dragon S01E07 1080p WEB-DL", InfoHash: "wrong"},
		{Title: "House of the Dragon S01 1080p WEB-DL", InfoHash: "pack"},
		{Title: "House of the Dragon Season 1 Complete 1080p WEB-DL", InfoHash: "season-word"},
		{Title: "House of the Dragon S02E01 1080p WEB-DL", InfoHash: "wrong-season"},
	}

	got := filterResultsForMedia(results, MediaID{Series: true, Season: 1, Episode: 1})

	if len(got) != 3 {
		t.Fatalf("want exact episode plus two season packs, got %d: %+v", len(got), got)
	}
	want := map[string]bool{"exact": false, "pack": false, "season-word": false}
	for _, r := range got {
		if _, ok := want[r.InfoHash]; !ok {
			t.Fatalf("unexpected result kept: %+v", r)
		}
		want[r.InfoHash] = true
	}
	for hash, seen := range want {
		if !seen {
			t.Fatalf("missing kept result %q", hash)
		}
	}
}

func TestShouldWaitForFilesSkipsExactEpisodeRelease(t *testing.T) {
	mid := MediaID{Series: true, Season: 1, Episode: 1}

	if shouldWaitForFiles(rank.Parse("House of the Dragon S01E01 1080p WEB-DL"), mid) {
		t.Fatalf("exact episode release should not wait for TorrServer file list")
	}
	if !shouldWaitForFiles(rank.Parse("House of the Dragon S01 1080p WEB-DL"), mid) {
		t.Fatalf("season pack should wait for TorrServer file list")
	}
	if !shouldWaitForFiles(rank.Parse("House of the Dragon S01E02 1080p WEB-DL"), mid) {
		t.Fatalf("wrong episode should wait; filtering should normally drop it first")
	}
	if !shouldWaitForFiles(rank.Parse("Movie 2020 1080p WEB-DL"), MediaID{}) {
		t.Fatalf("movie should wait so largest video can be selected")
	}
}

func TestLazyPlayableHashForExactEpisodeWithInfoHash(t *testing.T) {
	c := rank.Candidate{
		Result: prowlarr.Result{InfoHash: "ABCDEF1234567890ABCDEF1234567890ABCDEF12"},
		Parsed: rank.Parse("Frieren S02E09 1080p WEB-DL"),
	}
	hash, ok := lazyPlayableHash(c, MediaID{Series: true, Season: 2, Episode: 9})
	if !ok {
		t.Fatal("expected exact episode with infohash to be lazy-playable")
	}
	if hash != "abcdef1234567890abcdef1234567890abcdef12" {
		t.Fatalf("hash = %q", hash)
	}
}

func TestLazyPlayableHashRejectsSeasonPack(t *testing.T) {
	c := rank.Candidate{
		Result: prowlarr.Result{InfoHash: "abcdef1234567890abcdef1234567890abcdef12"},
		Parsed: rank.Parse("Frieren S02 1080p WEB-DL"),
	}
	if _, ok := lazyPlayableHash(c, MediaID{Series: true, Season: 2, Episode: 9}); ok {
		t.Fatal("season pack must not be lazy-playable because file index is unknown")
	}
}

func TestPrioritizeExactEpisodeBeforeSeasonPacks(t *testing.T) {
	candidates := []rank.Candidate{
		{Result: prowlarr.Result{InfoHash: "pack-4k"}, Parsed: rank.Parse("House of the Dragon S01 2160p WEB-DL")},
		{Result: prowlarr.Result{InfoHash: "exact-1080"}, Parsed: rank.Parse("House of the Dragon S01E01 1080p WEB-DL")},
		{Result: prowlarr.Result{InfoHash: "pack-1080"}, Parsed: rank.Parse("House of the Dragon Season 1 Complete 1080p WEB-DL")},
		{Result: prowlarr.Result{InfoHash: "exact-720"}, Parsed: rank.Parse("House of the Dragon S01E01 720p WEB-DL")},
	}

	prioritizeExactEpisode(candidates, MediaID{Series: true, Season: 1, Episode: 1})

	got := []string{
		candidates[0].Result.InfoHash,
		candidates[1].Result.InfoHash,
		candidates[2].Result.InfoHash,
		candidates[3].Result.InfoHash,
	}
	want := []string{"exact-1080", "exact-720", "pack-4k", "pack-1080"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestPreloadTopOnlyPreloadsFirstStream(t *testing.T) {
	preloaded := make(chan string, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/stream/stream" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		link, _ := url.QueryUnescape(r.URL.Query().Get("link"))
		preloaded <- link
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	r := &Resolver{ts: torrserver.New(ts.URL), preload: true}
	r.preloadTop(context.Background(), []Stream{
		{Hash: "first", FileIndex: 1},
		{Hash: "second", FileIndex: 1},
	})

	select {
	case got := <-preloaded:
		if got != "first" {
			t.Fatalf("preloaded %q, want first", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for preload")
	}

	select {
	case got := <-preloaded:
		t.Fatalf("unexpected extra preload for %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// A slow/broad pack query failing must not discard the successful episode query.
func TestResolveSeriesPartialFailureStillReturns(t *testing.T) {
	cine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"name":"Some Show","releaseInfo":"2015"}}`))
	}))
	defer cine.Close()

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if q == "Some Show S02" { // the pack query fails
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeReleases(w, []prowlarr.Result{
			{Title: "Some Show S02E05 1080p WEB x265", Seeders: 40, Size: 2e9, InfoHash: "ep", Protocol: "torrent"},
		})
	}))
	defer prow.Close()

	r := New(
		cinemeta.New(cine.URL, fakeCache{}, time.Hour),
		prowlarr.New(prow.URL, "k", fakeCache{}, time.Hour, false),
		nil, nil, false,
	)
	got, _, err := r.Candidates(context.Background(), "series", "tt1234567:2:5", config.Default())
	if err != nil {
		t.Fatalf("resolve should tolerate one failed query, got: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 candidate from the surviving query, got %d", len(got))
	}
}

// Non-torrent results are dropped (TorrServer is torrent-only).
func TestResolveDropsNonTorrent(t *testing.T) {
	cine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"name":"Movie","year":"2020"}}`))
	}))
	defer cine.Close()
	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReleases(w, []prowlarr.Result{
			{Title: "Movie 2020 1080p x264", Seeders: 50, Size: 5e9, InfoHash: "a", Protocol: "usenet"},
			{Title: "Movie 2020 1080p x265", Seeders: 60, Size: 5e9, InfoHash: "b", Protocol: "torrent"},
		})
	}))
	defer prow.Close()
	r := New(
		cinemeta.New(cine.URL, fakeCache{}, time.Hour),
		prowlarr.New(prow.URL, "k", fakeCache{}, time.Hour, false),
		nil, nil, false,
	)
	got, _, err := r.Candidates(context.Background(), "movie", "tt1", config.Default())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].Result.InfoHash != "b" {
		t.Fatalf("want only the torrent result, got %+v", got)
	}
}

func writeReleases(w http.ResponseWriter, results []prowlarr.Result) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(results)
}
