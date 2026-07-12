// Package server wires the chi router and owns the HTTP lifecycle.
package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"family-torrserver/internal/settings"
	"family-torrserver/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server holds shared dependencies for the HTTP handlers.
type Server struct {
	set   *settings.Settings
	store *store.Store
	http  *http.Server
}

// New builds a Server with its router wired.
func New(set *settings.Settings, st *store.Store) *Server {
	s := &Server{set: set, store: st}
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

	// Per-user routes. Token-validation middleware arrives in milestone 2;
	// for now the manifest is served statically for any token.
	r.Route("/u/{token}", func(r chi.Router) {
		r.Get("/manifest.json", s.handleManifest)
	})

	return r
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) Run(ctx context.Context) error {
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
