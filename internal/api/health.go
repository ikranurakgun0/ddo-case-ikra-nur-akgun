package api

import (
	"context"
	"net/http"
	"time"

	"esnafdefter/internal/httpx"
)

// handleHealth veritabanı bağlantısı dahil sağlık durumunu döner. DB yoksa 503.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(ctx); err != nil {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "db": "down"})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "db": "up"})
}
