package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/resolver"
	"github.com/simn03/nuvio-p2p-http-addon/internal/sign"

	"github.com/go-chi/chi/v5"
)

// handlePlay verifies the HMAC signature and reverse-proxies to TorrServer's
// stream endpoint, passing Range through for seeking (§10, §11, §15). It is not
// under /u/* — the signature + expiry are the access control.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	idxStr := chi.URLParam(r, "idx")
	index, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "bad index", http.StatusBadRequest)
		return
	}
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		http.Error(w, "bad exp", http.StatusBadRequest)
		return
	}
	sig := r.URL.Query().Get("sig")
	// season/episode are present only for deferred season packs; absent => 0.
	season, _ := strconv.Atoi(r.URL.Query().Get("s"))
	episode, _ := strconv.Atoi(r.URL.Query().Get("e"))

	switch err := s.signer.Verify(hash, index, season, episode, exp, sig, time.Now().UnixMilli()); err {
	case nil:
		// ok
	case sign.ErrExpired:
		slog.WarnContext(r.Context(), "play: link expired", "hash", hash, "index", index)
		http.Error(w, "link expired", http.StatusGone)
		return
	default:
		slog.WarnContext(r.Context(), "play: bad signature", "hash", hash, "index", index)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// EnsureAdded returns the torrent's real infohash: for no-infohash
	// candidates the signed token is synthetic and only resolves to the real
	// hash here, when the link is actually added.
	realHash, err := s.torr.EnsureAdded(r.Context(), hash)
	if err != nil {
		slog.ErrorContext(r.Context(), "play: lazy add failed", "hash", hash, "index", index, "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Track successful adds even when a probe or metadata lookup returns early.
	s.sweeper.Touch(realHash)

	// Deferred season pack: resolve the episode's file now (memoized), instead
	// of blocking every candidate on TorrServer during stream-list generation.
	if index == resolver.AutoSelectFile {
		resolved, err := s.resolver.PlayFileIndex(r.Context(), realHash, season, episode)
		if err != nil {
			slog.WarnContext(r.Context(), "play: file selection fell back to 1", "hash", realHash, "season", season, "episode", episode, "err", err)
		}
		index = resolved
	}

	// TorrServer's stream endpoint has no HEAD handler. Answer from metadata
	// rather than forwarding HEAD (405) or downloading a GET body for a HEAD.
	if r.Method == http.MethodHead {
		files, err := s.torr.EnsureFiles(r.Context(), realHash, 10*time.Second)
		if err != nil {
			http.Error(w, "metadata unavailable", http.StatusBadGateway)
			return
		}
		for _, f := range files {
			if f.ID == index {
				w.Header().Set("Content-Type", mediaType(f.Path))
				w.Header().Set("Content-Length", strconv.FormatInt(f.Length, 10))
				w.Header().Set("Accept-Ranges", "bytes")
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		http.Error(w, "file metadata unavailable", http.StatusServiceUnavailable)
		return
	}
	target, err := url.Parse(s.torr.StreamURL(realHash, index))
	if err != nil {
		http.Error(w, "bad target", http.StatusInternalServerError)
		return
	}

	start := time.Now()
	slog.DebugContext(r.Context(), "play: proxying", "hash", hash, "index", index, "range", r.Header.Get("Range"))
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = target.Path
			req.URL.RawQuery = target.RawQuery
			req.Host = target.Host
			// Range and other client headers pass through unchanged, so
			// TorrServer serves partial content for seeking.
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, e error) {
			if errors.Is(e, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
				slog.InfoContext(r.Context(), "play: client canceled", "hash", hash, "index", index, "dur", time.Since(start))
				return
			}
			slog.ErrorContext(r.Context(), "play: proxy error", "hash", hash, "index", index, "dur", time.Since(start), "err", e)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// buildPlayURL constructs the absolute signed /play URL for a stream, using
// PUBLIC_HOST when set and otherwise the request Host. season/episode are 0
// except for deferred season packs, where /play needs them to pick the file.
func (s *Server) buildPlayURL(r *http.Request, hash string, index, season, episode int) string {
	exp := time.Now().Add(s.set.PlayURLTTL).UnixMilli()
	// Idle torrent eviction must not discard the source of a still-valid URL.
	s.torr.RetainAddLink(hash, time.UnixMilli(exp))
	path := s.signer.PlayPath(hash, index, season, episode, exp)

	host := s.set.PublicHost
	scheme := "https"
	if host == "" {
		host = r.Host
		if r.TLS == nil {
			scheme = "http"
		}
	}
	// PUBLIC_HOST may already include a scheme.
	if hasScheme(host) {
		return host + path
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

func hasScheme(h string) bool {
	return len(h) > 7 && (h[:7] == "http://" || (len(h) > 8 && h[:8] == "https://"))
}
