package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/httpx"
)

type tokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	Role         auth.Role `json:"role"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "e-posta ve parola zorunlu")
		return
	}

	var (
		userID, businessID, hash string
		role                     auth.Role
	)
	err := s.pool.QueryRow(r.Context(),
		`SELECT id::text, business_id::text, password_hash, role
		   FROM users WHERE lower(email) = lower($1) AND active`, req.Email).
		Scan(&userID, &businessID, &hash, &role)
	notFound := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !notFound {
		s.log.Error("login sorgusu", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	if notFound {
		hash = s.dummyHash
	}
	ok, _ := auth.VerifyPassword(req.Password, hash)
	if notFound || !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "e-posta veya parola hatalı")
		return
	}

	resp, err := s.issueTokens(r.Context(), userID, businessID, role)
	if err != nil {
		s.log.Error("belirteç üretimi", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	_, _ = s.pool.Exec(r.Context(),
		`INSERT INTO audit_log(business_id, actor_id, action, entity, entity_id)
		 VALUES ($1::uuid, $2::uuid, 'login', 'user', $2)`, businessID, userID)
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) issueTokens(ctx context.Context, userID, businessID string, role auth.Role) (tokenResponse, error) {
	access, err := s.issuer.IssueAccess(userID, businessID, role)
	if err != nil {
		return tokenResponse{}, err
	}
	plain, hash, err := auth.NewRefreshToken()
	if err != nil {
		return tokenResponse{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens(user_id, token_hash, expires_at) VALUES ($1::uuid, $2, $3)`,
		userID, hash, time.Now().Add(s.refreshTTL)); err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{AccessToken: access, RefreshToken: plain, TokenType: "Bearer",
		ExpiresIn: int(s.issuer.TTL().Seconds()), Role: role}, nil
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// handleRefresh yenileme belirtecini döndürür (rotation): eski belirteç iptal edilir.
// İptal edilmiş bir belirteç tekrar sunulursa çalınmış sayılır ve kullanıcının tüm
// oturumları kapatılır.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.RefreshToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "refresh_token zorunlu")
		return
	}
	hash := auth.HashRefreshToken(req.RefreshToken)

	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	var (
		tokenID, userID, businessID string
		expires                     time.Time
		revoked                     *time.Time
		role                        auth.Role
		active                      bool
	)
	err = tx.QueryRow(r.Context(),
		`SELECT t.id::text, t.user_id::text, t.expires_at, t.revoked_at, u.business_id::text, u.role, u.active
		   FROM refresh_tokens t JOIN users u ON u.id = t.user_id
		  WHERE t.token_hash = $1 FOR UPDATE OF t`, hash).
		Scan(&tokenID, &userID, &expires, &revoked, &businessID, &role, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token", "belirteç geçersiz")
		return
	}
	if err != nil {
		s.log.Error("refresh sorgusu", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	if revoked != nil {
		// Yeniden kullanım tespiti: tüm oturumları kapat ve kalıcı yap.
		_, _ = tx.Exec(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1::uuid AND revoked_at IS NULL`, userID)
		_ = tx.Commit(r.Context())
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token", "belirteç geçersiz")
		return
	}
	if !active || time.Now().After(expires) {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_token", "belirteç geçersiz veya süresi dolmuş")
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1::uuid`, tokenID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	resp, err := s.issueTokens(r.Context(), userID, businessID, role)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.RefreshToken != "" {
		_, _ = s.pool.Exec(r.Context(),
			`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			auth.HashRefreshToken(req.RefreshToken))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c, _ := httpx.ClaimsFrom(r.Context())
	out := map[string]any{"user_id": c.UserID, "business_id": c.BusinessID, "role": c.Role}
	var email, bizName string
	var contactID *string
	if err := s.pool.QueryRow(r.Context(),
		`SELECT u.email, b.name, u.contact_id::text FROM users u JOIN businesses b ON b.id = u.business_id WHERE u.id = $1::uuid`,
		c.UserID).Scan(&email, &bizName, &contactID); err == nil {
		out["email"], out["business_name"], out["contact_id"] = email, bizName, contactID
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
