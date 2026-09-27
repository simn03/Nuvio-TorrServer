package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

func TestHeadOnlyTorrentIsSwept(t *testing.T) {
	for _, metadataAvailable := range []bool{true, false} {
		name := "metadata-error"
		if metadataAvailable {
			name = "metadata-success"
		}
		t.Run(name, func(t *testing.T) {
			removed := make(chan string, 1)
			var adds atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/torrents" {
					t.Error("HEAD opened a media stream")
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				switch body["action"] {
				case "add":
					adds.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"hash": "realhash"})
				case "get":
					if !metadataAvailable {
						http.Error(w, "metadata failed", http.StatusBadGateway)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"hash":       "realhash",
						"file_stats": []torrserver.File{{ID: 1, Path: "film.mkv", Length: 123}},
					})
				case "rem":
					removed <- body["hash"].(string)
				default:
					t.Errorf("unexpected action %v", body["action"])
				}
			}))
			defer upstream.Close()
			s := newPlayServer(upstream.URL)
			s.sweeper = torrserver.NewSweeper(s.torr, -time.Second)
			s.torr.RegisterAddLink("synthetic", "https://example.test/movie.torrent")
			path := s.signer.PlayPath("synthetic", 1, 0, 0, time.Now().Add(time.Hour).UnixMilli())
			rec := httptest.NewRecorder()
			playRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, path, nil))
			wantStatus := http.StatusBadGateway
			if metadataAvailable {
				wantStatus = http.StatusOK
			}
			if rec.Code != wantStatus || adds.Load() != 1 {
				t.Fatalf("HEAD status=%d adds=%d", rec.Code, adds.Load())
			}
			s.sweeper.Sweep(context.Background())
			select {
			case hash := <-removed:
				if hash != "realhash" {
					t.Fatalf("removed %q, want resolved realhash", hash)
				}
			default:
				t.Fatal("HEAD-only torrent escaped idle cleanup")
			}
		})
	}
}

func TestPlayURLRetainsSourceUntilSignedExpiry(t *testing.T) {
	var adds atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["action"] == "add" {
			adds.Add(1)
			if body["link"] != "https://example.test/movie.torrent" {
				t.Errorf("source link = %v", body["link"])
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hash":       "realhash",
			"file_stats": []torrserver.File{{ID: 1, Path: "film.mkv", Length: 123}},
		})
	}))
	defer upstream.Close()
	s := newPlayServer(upstream.URL)
	s.torr = torrserver.New(upstream.URL, torrserver.WithAddLinkTTL(time.Minute))
	s.sweeper = torrserver.NewSweeper(s.torr, time.Hour)
	s.torr.RegisterAddLink("synthetic", "https://example.test/movie.torrent")
	url := s.buildPlayURL(httptest.NewRequest(http.MethodGet, "/", nil), "synthetic", 1, 0, 0)

	// Reclaim candidates after their initial registration TTL, while the issued
	// one-hour playback URL remains valid.
	s.torr.PurgeExpiredLinks(time.Now().Add(2 * time.Minute))
	rec := httptest.NewRecorder()
	playRouter(s).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, url, nil))
	if rec.Code != http.StatusOK || adds.Load() != 1 {
		t.Fatalf("valid URL lost its source: status=%d adds=%d", rec.Code, adds.Load())
	}
}
