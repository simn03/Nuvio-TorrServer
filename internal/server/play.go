package server

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"nuvio-torrserver/internal/sign"

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

	switch err := s.signer.Verify(hash, index, exp, sig, time.Now().UnixMilli()); err {
	case nil:
		// ok
	case sign.ErrExpired:
		http.Error(w, "link expired", http.StatusGone)
		return
	default:
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Update last-access so the sweeper keeps this torrent alive while playing.
	s.sweeper.Touch(hash)

	target, err := url.Parse(s.torr.StreamURL(hash, index))
	if err != nil {
		http.Error(w, "bad target", http.StatusInternalServerError)
		return
	}

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
			log.Printf("play proxy error (hash=%s idx=%d): %v", hash, index, e)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

// buildPlayURL constructs the absolute signed /play URL for a stream, using
// PUBLIC_HOST when set and otherwise the request Host.
func (s *Server) buildPlayURL(r *http.Request, hash string, index int) string {
	exp := time.Now().Add(s.set.PlayURLTTL).UnixMilli()
	path := s.signer.PlayPath(hash, index, exp)

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
