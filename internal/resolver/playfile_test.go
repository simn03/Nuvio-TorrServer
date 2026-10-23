package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

type mapCache map[string]int

func (m mapCache) EnrichCacheGet(key string) (int, bool) { v, ok := m[key]; return v, ok }
func (m mapCache) EnrichCacheSet(key string, index int, _ time.Duration) error {
	m[key] = index
	return nil
}

// filesServer is a mock TorrServer whose "get" returns files.
func filesServer(t *testing.T, files []map[string]any) *torrserver.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrents" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hash": "h", "file_stats": files})
	}))
	t.Cleanup(srv.Close)
	return torrserver.New(srv.URL)
}

// A fake release whose only file is an .exe must be refused and remembered.
func TestPlayFileIndexRejectsTorrentWithoutVideo(t *testing.T) {
	cache := mapCache{}
	r := &Resolver{ts: filesServer(t, []map[string]any{
		{"id": 1, "path": "Show S01E07 1080p WEB H264-GRP.exe", "length": 1_270_441_586},
	}), enrichCache: cache}

	if _, err := r.PlayFileIndex(context.Background(), "h", 1, 0, 0); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("err = %v, want ErrNoVideo", err)
	}
	if !r.knownNoVideo("h") {
		t.Error("torrent not recorded as having no video")
	}
	// Cached: a second call refuses without asking TorrServer again.
	r.ts = torrserver.New("http://127.0.0.1:1")
	if _, err := r.PlayFileIndex(context.Background(), "h", 1, 0, 0); !errors.Is(err, ErrNoVideo) {
		t.Fatalf("cached err = %v, want ErrNoVideo", err)
	}
}

// File 1 isn't always the video (e.g. subtitles first); pick the video.
func TestPlayFileIndexReplacesNonVideoIndex(t *testing.T) {
	r := &Resolver{ts: filesServer(t, []map[string]any{
		{"id": 1, "path": "Movie/Movie.srt", "length": 40_000},
		{"id": 2, "path": "Movie/Movie.2020.1080p.mkv", "length": 8_000_000_000},
	}), enrichCache: mapCache{}}

	got, err := r.PlayFileIndex(context.Background(), "h", 1, 0, 0)
	if err != nil || got != 2 {
		t.Fatalf("PlayFileIndex = %d, %v; want 2, nil", got, err)
	}
}

func TestPlayFileIndexKeepsVideoIndex(t *testing.T) {
	r := &Resolver{ts: filesServer(t, []map[string]any{
		{"id": 1, "path": "Show.S01E01.mkv", "length": 100},
		{"id": 2, "path": "Show.S01E01.sample.mkv", "length": 999}, // larger, but index 1 is a video
	})}

	got, err := r.PlayFileIndex(context.Background(), "h", 1, 0, 0)
	if err != nil || got != 1 {
		t.Fatalf("PlayFileIndex = %d, %v; want 1, nil", got, err)
	}
}

// Explicit indexes on the same torrent are memoized separately.
func TestPlayFileIndexCachesPerExplicitIndex(t *testing.T) {
	r := &Resolver{ts: filesServer(t, []map[string]any{
		{"id": 3, "path": "Show.S01E03.mkv", "length": 100},
		{"id": 5, "path": "Show.S01E05.mkv", "length": 100},
	}), enrichCache: mapCache{}}

	for _, want := range []int{3, 5, 3} {
		if got, err := r.PlayFileIndex(context.Background(), "h", want, 0, 0); err != nil || got != want {
			t.Fatalf("PlayFileIndex(index %d) = %d, %v", want, got, err)
		}
	}
}
