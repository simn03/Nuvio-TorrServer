package torrserver

import (
	"context"
	"encoding/json"
	"errors"
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

func TestEnsureAddedDoesNotBlockOtherTokens(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["link"] == "slow-source" {
			close(started)
			<-release
		}
		_ = json.NewEncoder(w).Encode(torrent{Hash: body["link"].(string)})
	}))
	defer srv.Close()
	defer close(release)
	c := New(srv.URL)
	c.RegisterAddLink("slow", "slow-source")
	c.RegisterAddLink("fast", "fast-source")
	slowDone := make(chan error, 1)
	go func() {
		_, err := c.EnsureAdded(context.Background(), "slow")
		slowDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow request did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := c.EnsureAdded(ctx, "fast"); err != nil || got != "fast-source" {
		t.Fatalf("unrelated token blocked: %q, %v", got, err)
	}
	select {
	case err := <-slowDone:
		t.Fatalf("slow request unexpectedly completed: %v", err)
	default:
	}
}

func TestEnsureAddedCancelledWaiter(t *testing.T) {
	c := New("http://unused.test")
	unlock, err := c.lockAdd(context.Background(), "hash:same-token")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.EnsureAdded(ctx, "same-token")
		done <- err
	}()
	deadline := time.After(time.Second)
	for {
		c.mu.RLock()
		refs := c.adding["hash:same-token"].refs
		c.mu.RUnlock()
		if refs == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("waiter did not reach the per-token lock")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled request still waiting for another request")
	}
	c.mu.RLock()
	refs := c.adding["hash:same-token"].refs
	c.mu.RUnlock()
	if refs != 1 {
		t.Fatalf("cancelled waiter retained a lock reference: %d", refs)
	}
}

func TestEnsureAddedCancellationDoesNotInvalidateCachedTorrent(t *testing.T) {
	var adds atomic.Int32
	var gets atomic.Int32
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "add" {
			adds.Add(1)
		}
		if body["action"] == "get" && gets.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realhash"})
	}))
	defer srv.Close()
	c := New(srv.URL)
	c.RegisterAddLink("synthetic", "source")
	if _, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := c.EnsureAdded(ctx, "synthetic")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cached get did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled owner: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled get did not finish")
	}
	if got, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil || got != "realhash" {
		t.Fatalf("next request: %q, %v", got, err)
	}
	if adds.Load() != 1 {
		t.Fatalf("cancellation caused %d add requests", adds.Load())
	}
	if len(c.adding) != 0 {
		t.Fatalf("idle per-token locks retained: %d", len(c.adding))
	}
}

func TestPurgeExpiredLinksIncludesUnplayedCandidatesAndAliases(t *testing.T) {
	srv, _ := mockTS(t, nil)
	defer srv.Close()
	c := New(srv.URL, WithAddLinkTTL(time.Hour))
	c.RegisterAddLink("unplayed", "unplayed-source")
	c.RegisterAddLink("synthetic", "played-source")
	if _, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	if len(c.addLink) != 3 || len(c.added) != 2 {
		t.Fatalf("missing test candidates or aliases: links=%d added=%d", len(c.addLink), len(c.added))
	}
	if !c.addLink["deadbeef"].expiresAt.Equal(c.addLink["synthetic"].expiresAt) {
		t.Fatal("resolved alias did not inherit source expiry")
	}
	c.PurgeExpiredLinks(time.Now().Add(time.Hour))
	if len(c.addLink) != 0 || len(c.added) != 0 || len(c.adding) != 0 {
		t.Fatalf("expired bookkeeping retained: links=%d added=%d locks=%d", len(c.addLink), len(c.added), len(c.adding))
	}
}

func TestRemoveRetainsSourceUntilSignedURLExpires(t *testing.T) {
	srv, actions := mockTS(t, nil)
	defer srv.Close()
	c := New(srv.URL, WithAddLinkTTL(time.Minute))
	c.RegisterAddLink("synthetic", "source")
	if _, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(6 * time.Hour)
	c.RetainAddLink("synthetic", until)
	c.RegisterAddLink("synthetic", "source")
	if !c.addLink["synthetic"].expiresAt.Equal(until) || !c.addLink["deadbeef"].expiresAt.Equal(until) {
		t.Fatal("registration shortened signed URL retention or failed to retain alias")
	}
	if err := c.Remove(context.Background(), "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if len(c.added) != 0 {
		t.Fatalf("Remove retained added aliases: %v", c.added)
	}
	c.PurgeExpiredLinks(time.Now().Add(2 * time.Hour))
	if got, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil || got != "deadbeef" {
		t.Fatalf("synthetic URL could not re-add removed torrent: %q, %v", got, err)
	}
	if got := strings.Join(*actions, ","); got != "add,rem,add" {
		t.Fatalf("actions = %s, want add,rem,add", got)
	}
	if !c.addLink["deadbeef"].expiresAt.Equal(until) {
		t.Fatal("repeated playback changed the source expiry")
	}
	c.PurgeExpiredLinks(until)
	if len(c.addLink) != 0 || len(c.added) != 0 {
		t.Fatalf("expired signed URL retained bookkeeping: links=%d added=%d", len(c.addLink), len(c.added))
	}
}

func TestEnsureAddedDeduplicatesAliasesAfterEviction(t *testing.T) {
	var adds atomic.Int32
	var present atomic.Bool
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "add" {
			if adds.Add(1) == 2 {
				close(started)
				<-release
			}
			present.Store(true)
		}
		if !present.Load() {
			http.Error(w, "torrent not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realhash"})
	}))
	defer srv.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	c := New(srv.URL)
	c.RegisterAddLink("synthetic", "source")
	if _, err := c.EnsureAdded(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	present.Store(false)
	done := make(chan error, 2)
	go func() {
		_, err := c.EnsureAdded(context.Background(), "synthetic")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("re-add did not start")
	}
	go func() {
		_, err := c.EnsureAdded(context.Background(), "realhash")
		done <- err
	}()
	deadline := time.After(time.Second)
	for {
		c.mu.RLock()
		refs := c.adding["source:source"].refs
		c.mu.RUnlock()
		if refs == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("real-hash request did not join the source lock")
		case <-time.After(time.Millisecond):
		}
	}
	releaseOnce.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("alias request did not complete")
		}
	}
	if adds.Load() != 2 {
		t.Fatalf("aliases caused %d adds, want initial add and one re-add", adds.Load())
	}
	if len(c.adding) != 0 {
		t.Fatalf("idle source locks retained: %d", len(c.adding))
	}
}

func TestEnsureAddedDoesNotRestorePurgedLinks(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(torrent{Hash: "realhash"})
	}))
	defer srv.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	c := New(srv.URL, WithAddLinkTTL(time.Minute))
	c.RegisterAddLink("synthetic", "source")
	done := make(chan error, 1)
	go func() {
		_, err := c.EnsureAdded(context.Background(), "synthetic")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("add did not start")
	}
	c.PurgeExpiredLinks(time.Now().Add(time.Hour))
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("add did not complete")
	}
	if len(c.addLink) != 0 || len(c.added) != 0 || len(c.adding) != 0 {
		t.Fatalf("completed add resurrected purged bookkeeping: links=%d added=%d locks=%d", len(c.addLink), len(c.added), len(c.adding))
	}
}
