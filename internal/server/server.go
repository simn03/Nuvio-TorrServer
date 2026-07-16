// Package server wires the chi router and owns the HTTP lifecycle.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/cinemeta"
	"github.com/simn03/nuvio-p2p-http-addon/internal/logging"
	"github.com/simn03/nuvio-p2p-http-addon/internal/prowlarr"
	"github.com/simn03/nuvio-p2p-http-addon/internal/resolver"
	"github.com/simn03/nuvio-p2p-http-addon/internal/settings"
	"github.com/simn03/nuvio-p2p-http-addon/internal/sign"
	"github.com/simn03/nuvio-p2p-http-addon/internal/store"
	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	set      *settings.Settings
	store    *store.Store
	resolver *resolver.Resolver
	prow     *prowlarr.Client
	torr     *torrserver.Client
	sweeper  *torrserver.Sweeper
	signer   *sign.Signer
	http     *http.Server
}

// New builds a Server with its router and service clients wired.
func New(set *settings.Settings, st *store.Store) *Server {
	cine := cinemeta.New(set.CinemetaURL, st, set.CinemetaCacheTTL)
	prow := prowlarr.New(set.ProwlarrURL, set.ProwlarrAPIKey, st, set.ProwlarrCacheTTL, set.ProwlarrInsecureTLS)
	torr := torrserver.New(set.TorrServerURL)
	sweeper := torrserver.NewSweeper(torr, set.TorrentIdleTTL)

	s := &Server{
		set:   set,
		store: st,
		resolver: resolver.New(cine, prow, torr, sweeper, set.TorrServerPreload,
			resolver.WithSearchTimeout(set.ProwlarrSearchTimeout),
			resolver.WithEnrichCache(st),
		),
		prow:    prow,
		torr:    torr,
		sweeper: sweeper,
		signer:  sign.New(set.SigningSecret),
	}
	s.http = &http.Server{
		Addr:              ":" + set.Port,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger)
	r.Use(corsStremio)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Per-user routes: every /u/* request is token-validated (403 on
	// missing/revoked).
	r.Route("/u/{token}", func(r chi.Router) {
		r.Use(s.requireToken)
		r.Get("/manifest.json", s.handleManifest)
		r.Get("/configure", s.handleConfigureGet)
		r.Post("/configure", s.handleConfigurePost)
		r.Get("/stream/{type}/{id}.json", s.handleStream)
	})

	// Signed play proxy — not under /u/*; guarded by HMAC + expiry.
	r.Get("/play/{hash}/{idx}", s.handlePlay)

	return r
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) Run(ctx context.Context) error {
	go s.purgeCacheLoop(ctx)
	go s.sweeper.Run(ctx)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("addon listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			slog.Error("server exited", "err", err)
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		slog.Info("shutting down")
		return s.http.Shutdown(shutdownCtx)
	}
}

// purgeCacheLoop periodically reclaims expired cache rows (lazy-expire on read
// handles correctness; this bounds table growth).
func (s *Server) purgeCacheLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := s.store.PurgeExpiredCache(); err != nil {
				slog.Error("cache purge failed", "err", err)
			} else if n > 0 {
				slog.Info("purged expired cache rows", "count", n)
			}
		}
	}
}

// requestLogger logs every request's method, path, status, response size, and
// duration, tagged with the chi request id, and stashes that id into context
// so downstream slog calls (resolver, prowlarr, torrserver, ...) inherit it —
// this is the primary tool for profiling where pipeline time goes.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.GetReqID(r.Context())
		ctx := logging.WithRequestID(r.Context(), reqID)
		r = r.WithContext(ctx)

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			slog.InfoContext(ctx, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur", time.Since(start),
				"remote", r.RemoteAddr,
			)
		}()
		next.ServeHTTP(ww, r)
	})
}

// corsStremio applies the permissive CORS headers Stremio expects.
func corsStremio(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
