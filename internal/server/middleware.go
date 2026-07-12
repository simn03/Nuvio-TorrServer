package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// requireToken validates the {token} path param against the store on every
// /u/* route, returning 403 for missing/unknown/revoked tokens (§10).
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		if token == "" || !s.store.IsValid(token) {
			http.Error(w, "forbidden: invalid or revoked token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
