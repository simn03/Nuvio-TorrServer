// Package server wires the chi router and owns the HTTP lifecycle.
package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"nuvio-torrserver/internal/cinemeta"
	"nuvio-torrserver/internal/prowlarr"
	"nuvio-torrserver/internal/resolver"
	"nuvio-torrserver/internal/settings"
	"nuvio-torrserver/internal/sign"
	"nuvio-torrserver/internal/store"
	"nuvio-torrserver/internal/torrserver"

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
		set:      set,
		store:    st,
		resolver: resolver.New(cine, prow, torr, sweeper, set.TorrServerPreload),
		prow:     prow,
		torr:     torr,
		sweeper:  sweeper,
		signer:   sign.New(set.SigningSecret),
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
		log.Printf("addon listening on %s", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		log.Printf("shutting down...")
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
				log.Printf("cache purge error: %v", err)
			} else if n > 0 {
				log.Printf("purged %d expired cache rows", n)
			}
		}
	}
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
