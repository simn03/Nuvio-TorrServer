package resolver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

func TestPreloadTopRegistersAddedTorrentForCleanup(t *testing.T) {
	for _, failPreload := range []bool{false, true} {
		name := "success"
		if failPreload {
			name = "preload connection fails"
		}
		t.Run(name, func(t *testing.T) {
			preloadRequested := make(chan struct{})
			var preloadOnce sync.Once
			removed := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/torrents":
					var body struct {
						Action string `json:"action"`
						Hash   string `json:"hash"`
					}
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					switch body.Action {
					case "add":
						_ = json.NewEncoder(w).Encode(map[string]string{"hash": "realhash"})
					case "rem":
						removed <- body.Hash
					default:
						t.Errorf("unexpected torrent action %q", body.Action)
						w.WriteHeader(http.StatusBadRequest)
					}
				case "/stream/stream":
					if got := req.URL.Query().Get("link"); got != "realhash" {
						t.Errorf("preload hash = %q, want realhash", got)
					}
					preloadOnce.Do(func() { close(preloadRequested) })
					if failPreload {
						// Closing without a response makes http.Client.Do fail.
						panic(http.ErrAbortHandler)
					}
					_, _ = w.Write([]byte("buffered"))
				default:
					t.Errorf("unexpected path %s", req.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer upstream.Close()

			client := torrserver.New(upstream.URL)
			client.RegisterAddLink("synthetic", "https://example.test/file.torrent")
			// Every tracked torrent is already idle, so cleanup needs no sleep.
			sweeper := torrserver.NewSweeper(client, -time.Second)
			r := &Resolver{ts: client, sweeper: sweeper, preload: true}
			r.preloadTop(context.Background(), []Stream{{Hash: "synthetic", FileIndex: 1}})
			select {
			case <-preloadRequested:
			case <-time.After(5 * time.Second):
				t.Fatal("preload did not reach upstream")
			}

			// Registration must precede preloading: even a failed preload needs
			// to release the torrent added while building the stream list.
			sweeper.Sweep(context.Background())
			select {
			case hash := <-removed:
				if hash != "realhash" {
					t.Fatalf("removed %q, want resolved hash realhash", hash)
				}
			default:
				t.Fatal("preload-only torrent was not removed by idle cleanup")
			}
		})
	}
}
