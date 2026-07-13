package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/settings"
	"github.com/simn03/nuvio-p2p-http-addon/internal/sign"
	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"

	"github.com/go-chi/chi/v5"
)

// newPlayServer builds a minimal Server wired only for the /play path, pointing
// at a mock TorrServer upstream.
func newPlayServer(upstreamURL string) *Server {
	set := &settings.Settings{PlayURLTTL: time.Hour, PublicHost: "addon.example.com"}
	torr := torrserver.New(upstreamURL)
	return &Server{
		set:     set,
		torr:    torr,
		sweeper: torrserver.NewSweeper(torr, time.Hour),
		signer:  sign.New("testsecret"),
	}
}

func playRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Get("/play/{hash}/{idx}", s.handlePlay)
	return r
}

func TestPlayProxiesWithRange(t *testing.T) {
	// Mock TorrServer: echoes back the Range header and serves 206.
	var gotRange string
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", "bytes 0-1023/50000")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("partialdata"))
	}))
	defer upstream.Close()

	s := newPlayServer(upstream.URL)
	exp := time.Now().Add(time.Hour).UnixMilli()
	path := s.signer.PlayPath("abchash", 6, exp)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	playRouter(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if gotRange != "bytes=0-1023" {
		t.Errorf("upstream Range = %q, want bytes=0-1023 (Range must pass through)", gotRange)
	}
	if rec.Header().Get("Content-Range") != "bytes 0-1023/50000" {
		t.Errorf("Content-Range not propagated: %q", rec.Header().Get("Content-Range"))
	}
	// Upstream URL should carry the TorrServer stream params.
	if gotQuery == "" || !contains(gotQuery, "link=abchash") || !contains(gotQuery, "index=6") {
		t.Errorf("upstream query = %q, want link=abchash&index=6&play", gotQuery)
	}
}

func TestPlayRejectsBadSigAndExpiry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream should not be hit for bad/expired sig")
	}))
	defer upstream.Close()
	s := newPlayServer(upstream.URL)

	// Bad signature -> 403.
	req := httptest.NewRequest(http.MethodGet, "/play/abchash/6?exp=99999999999999&sig=deadbeef", nil)
	rec := httptest.NewRecorder()
	playRouter(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad sig status = %d, want 403", rec.Code)
	}

	// Valid signature but expired -> 410.
	expired := time.Now().Add(-time.Minute).UnixMilli()
	path := s.signer.PlayPath("abchash", 6, expired)
	req = httptest.NewRequest(http.MethodGet, path, nil)
	rec = httptest.NewRecorder()
	playRouter(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Errorf("expired status = %d, want 410", rec.Code)
	}
}

func TestPlayLazyAddsRegisteredHashBeforeProxy(t *testing.T) {
	var actions []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/torrents":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			action, _ := req["action"].(string)
			actions = append(actions, action)
			_ = json.NewEncoder(w).Encode(map[string]any{"hash": "abchash"})
		case "/stream/stream":
			actions = append(actions, "stream")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("partialdata"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	s := newPlayServer(upstream.URL)
	s.torr.RegisterAddLink("abchash", "magnet:?xt=urn:btih:abchash")
	exp := time.Now().Add(time.Hour).UnixMilli()
	path := s.signer.PlayPath("abchash", 1, exp)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	playRouter(s).ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if len(actions) != 2 || actions[0] != "add" || actions[1] != "stream" {
		t.Fatalf("actions = %v, want [add stream]", actions)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
