package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"family-torrserver/internal/cinemeta"
	"family-torrserver/internal/config"
	"family-torrserver/internal/prowlarr"
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
