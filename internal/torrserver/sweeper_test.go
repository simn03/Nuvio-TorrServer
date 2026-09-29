package torrserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestSweeperRemovesIdle(t *testing.T) {
	var mu sync.Mutex
	var removed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if req["action"] == "rem" {
			mu.Lock()
			removed = append(removed, req["hash"].(string))
			mu.Unlock()
		}
	}))
	defer srv.Close()

	sw := NewSweeper(New(srv.URL), time.Minute) // 1m idle TTL
	sw.Touch("a")
	sw.Touch("b")

	// Sweep as if 2 minutes have passed: both exceed the 1m TTL -> removed.
	sw.sweep(context.Background(), time.Now().Add(2*time.Minute))

	mu.Lock()
	got := append([]string(nil), removed...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected both idle torrents removed, got %v", got)
	}
	if sw.tracked() != 0 {
		t.Errorf("tracked should be empty after sweep, got %d", sw.tracked())
	}
}

func TestSweeperKeepsFresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	sw := NewSweeper(New(srv.URL), time.Hour)
	sw.Touch("a")
	sw.Touch("b")
	sw.sweep(context.Background(), time.Now().Add(time.Minute)) // 1m < 1h TTL
	if sw.tracked() != 2 {
		t.Errorf("fresh entries should survive, tracked=%d", sw.tracked())
	}
}

func TestSweeperNilSafe(t *testing.T) {
	var sw *Sweeper
	sw.Touch("x") // must not panic
}

func TestSweeperExpiresUnplayedCandidates(t *testing.T) {
	client := New("http://unused.test", WithAddLinkTTL(time.Hour))
	client.RegisterAddLink("unplayed", "https://example.test/movie.torrent")
	sw := NewSweeper(client, time.Minute)
	sw.sweep(context.Background(), time.Now().Add(2*time.Hour))
	if len(client.addLink) != 0 {
		t.Fatal("unplayed candidate survived periodic cleanup")
	}
}
