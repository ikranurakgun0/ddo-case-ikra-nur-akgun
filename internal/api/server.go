// Package api HTTP uç noktalarını ve yönlendirmeyi içerir.
package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/httpx"
)

type Server struct {
	pool       *pgxpool.Pool
	log        *slog.Logger
	issuer     *auth.TokenIssuer
	refreshTTL timeDuration
	// Kullanıcı bulunamadığında da parola doğrulama süresi harcamak için (zamanlama farkından
	// hesap varlığının sızmasını azaltır).
	dummyHash string
}

func NewServer(pool *pgxpool.Pool, log *slog.Logger, issuer *auth.TokenIssuer, refreshTTL timeDuration) (*Server, error) {
	dummy, err := auth.HashPassword("dummy-password-for-timing")
	if err != nil {
		return nil, err
	}
	return &Server{pool: pool, log: log, issuer: issuer, refreshTTL: refreshTTL, dummyHash: dummy}, nil
}

// Routes tüm yönlendirmeyi kurar.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.Recover(s.log), httpx.Logging(s.log))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "kaynak bulunamadı")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "yöntem desteklenmiyor")
	})

	r.Get("/health", s.handleHealth)

	r.Route("/auth", func(r chi.Router) {
		r.Post("/login", s.handleLogin)
		r.Post("/refresh", s.handleRefresh)
		r.Post("/logout", s.handleLogout)
	})

	r.Group(func(r chi.Router) {
		r.Use(httpx.Authenticate(s.issuer))
		r.Get("/me", s.handleMe)
	})
	return r
}
