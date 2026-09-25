package torrserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockTS emulates the subset of TorrServer's /torrents API we use.
func mockTS(t *testing.T, files []File) (*httptest.Server, *[]string) {
	t.Helper()
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrents" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		action, _ := req["action"].(string)
		actions = append(actions, action)
		switch action {
		case "add", "get":
			_ = json.NewEncoder(w).Encode(torrent{Hash: "deadbeef", Title: "X", FileStats: files})
		case "rem":
			// empty body, 200
		default:
			http.Error(w, "bad action", http.StatusBadRequest)
		}
	}))
	return srv, &actions
}

func TestClientAddGetRemove(t *testing.T) {
	files := []File{{ID: 1, Path: "a.srt", Length: 10}, {ID: 2, Path: "movie.mkv", Length: 999}}
	srv, actions := mockTS(t, files)
	defer srv.Close()
	c := New(srv.URL)

	hash, got, err := c.Add(context.Background(), "magnet:?xt=urn:btih:deadbeef")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if hash != "deadbeef" || len(got) != 2 || got[1].ID != 2 {
		t.Fatalf("add returned hash=%s files=%+v", hash, got)
	}

	gf, err := c.Files(context.Background(), hash)
	if err != nil || len(gf) != 2 {
		t.Fatalf("files: %v %+v", err, gf)
	}

	if err := c.Remove(context.Background(), hash); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Join(*actions, ",") != "add,get,rem" {
		t.Errorf("actions = %v, want add,get,rem", *actions)
	}
}

func TestEnsureAddedUsesRegisteredLink(t *testing.T) {
	var gotLink string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/torrents" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		gotLink, _ = req["link"].(string)
		_ = json.NewEncoder(w).Encode(torrent{Hash: "abc123"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.RegisterAddLink("ABC123", "magnet:?xt=urn:btih:abc123")
	got, err := c.EnsureAdded(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("EnsureAdded: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("returned hash = %q, want abc123", got)
	}
	if gotLink != "magnet:?xt=urn:btih:abc123" {
		t.Fatalf("link = %q", gotLink)
	}
}

func TestEnsureAddedIgnoresUnknownHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected request for unknown hash")
	}))
	defer srv.Close()

	c := New(srv.URL)
	got, err := c.EnsureAdded(context.Background(), "missing")
	if err != nil {
		t.Fatalf("EnsureAdded: %v", err)
	}
	if got != "missing" {
		t.Fatalf("unknown hash should pass through unchanged, got %q", got)
	}
}

// A no-infohash candidate is registered under a synthetic token; EnsureAdded
// adds via its link and returns TorrServer's real infohash.
func TestEnsureAddedResolvesSyntheticToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realinfohash"})
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.RegisterAddLink("synth0000token", "http://prowlarr/download?apikey=x")
	got, err := c.EnsureAdded(context.Background(), "synth0000token")
	if err != nil {
		t.Fatalf("EnsureAdded: %v", err)
	}
	if got != "realinfohash" {
		t.Fatalf("returned hash = %q, want realinfohash", got)
	}
	// The real hash must now also resolve to the link (for repeat plays/sweeper).
	got2, err := c.EnsureAdded(context.Background(), "realinfohash")
	if err != nil || got2 != "realinfohash" {
		t.Fatalf("real hash re-add = %q, %v", got2, err)
	}
}

func TestEnsureFilesGivesUp(t *testing.T) {
	srv, _ := mockTS(t, nil) // always empty file list
	defer srv.Close()
	c := New(srv.URL)
	start := time.Now()
	files, err := c.EnsureFiles(context.Background(), "deadbeef", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty file list")
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("EnsureFiles returned too early; should have polled to the deadline")
	}
}

func TestStreamURL(t *testing.T) {
	c := New("http://127.0.0.1:8090/")
	got := c.StreamURL("abc def", 6)
	want := "http://127.0.0.1:8090/stream/stream?link=abc+def&index=6&play"
	if got != want {
		t.Errorf("StreamURL = %q, want %q", got, want)
	}
}

func TestConcurrentEnsureAdded(t *testing.T) {
	var adds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "add" {
			adds.Add(1)
			if body["link"] != "https://example.test/file.torrent" {
				t.Error("lost source link")
			}
		}
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realhash"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RegisterAddLink("synthetic", "https://example.test/file.torrent")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := c.EnsureAdded(context.Background(), "synthetic")
			if err != nil || h != "realhash" {
				t.Errorf("resolve: %s %v", h, err)
			}
		}()
	}
	wg.Wait()
	if adds.Load() != 1 {
		t.Fatalf("added %d times", adds.Load())
	}
}

func TestEnsureAddedReaddsEvictedTorrent(t *testing.T) {
	var adds atomic.Int32
	var present atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "add" {
			adds.Add(1)
			present.Store(true)
		}
		if !present.Load() {
			http.Error(w, "torrent not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realhash"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RegisterAddLink("synthetic", "https://example.test/file.torrent")
	for _, key := range []string{"synthetic", "realhash", "synthetic"} {
		h, err := c.EnsureAdded(context.Background(), key)
		if err != nil || h != "realhash" {
			t.Fatalf("EnsureAdded(%q) = %q, %v", key, h, err)
		}
	}
	if adds.Load() != 1 {
		t.Fatalf("added %d times before eviction", adds.Load())
	}
	present.Store(false)
	h, err := c.EnsureAdded(context.Background(), "synthetic")
	if err != nil || h != "realhash" {
		t.Fatalf("re-add = %q, %v", h, err)
	}
	if adds.Load() != 2 {
		t.Fatalf("added %d times after eviction", adds.Load())
	}
}
