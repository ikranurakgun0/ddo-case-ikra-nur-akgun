// Package httpx ortak HTTP yardımcıları: tutarlı hata biçimi, JSON, ara katmanlar.
package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"esnafdefter/internal/auth"
)

type ctxKey int

const (
	claimsKey ctxKey = iota
	requestIDKey
)

type errBody struct {
	Error errDetail `json:"error"`
}
type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError {"error":{"code":"...","message":"..."}} biçiminde yanıt yazar.
func WriteError(w http.ResponseWriter, status int, code, msg string) {
	WriteJSON(w, status, errBody{errDetail{Code: code, Message: msg}})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// DecodeJSON gövdeyi 1 MiB ile sınırlar ve bilinmeyen alanları reddeder.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_json", "istek gövdesi geçersiz")
		return false
	}
	return true
}

// ReadBody gövdeyi (en fazla 1 MiB) bayt olarak okur; idempotans özeti için ham bayt gerekir.
func ReadBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_json", "istek gövdesi okunamadı veya çok büyük")
		return nil, false
	}
	return b, true
}

// DecodeBytes ReadBody ile okunmuş gövdeyi çözer; bilinmeyen alanları reddeder.
func DecodeBytes(w http.ResponseWriter, b []byte, dst any) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		WriteError(w, http.StatusBadRequest, "invalid_json", "istek gövdesi geçersiz")
		return false
	}
	return true
}

func ClaimsFrom(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(claimsKey).(*auth.Claims)
	return c, ok
}

func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey).(string)
	return s
}

// Authenticate Authorization: Bearer <jwt> başlığını doğrular. Başarısızsa 401.
func Authenticate(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
				WriteError(w, http.StatusUnauthorized, "unauthenticated", "kimlik doğrulaması gerekli")
				return
			}
			c, err := issuer.ParseAccess(h[len(prefix):])
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "invalid_token", "belirteç geçersiz veya süresi dolmuş")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey, c)))
		})
	}
}

// RequireRole yalnızca verilen rollere izin verir; diğerleri 403 alır.
// Authenticate'ten SONRA kullanılmalıdır; kimlik yoksa 401 döner.
func RequireRole(roles ...auth.Role) func(http.Handler) http.Handler {
	allowed := make(map[auth.Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFrom(r.Context())
			if !ok {
				WriteError(w, http.StatusUnauthorized, "unauthenticated", "kimlik doğrulaması gerekli")
				return
			}
			if _, ok := allowed[c.Role]; !ok {
				WriteError(w, http.StatusForbidden, "forbidden", "bu işlem için yetkiniz yok")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

// Logging her isteği yapılandırılmış JSON olarak loglar. Başlık ve gövde ASLA loglanmaz
// (parola/belirteç sızıntısını önlemek için).
func Logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			id := hex.EncodeToString(b)
			w.Header().Set("X-Request-Id", id)
			sw := &statusWriter{ResponseWriter: w, status: 200}
			start := time.Now()
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			next.ServeHTTP(sw, r.WithContext(ctx))
			log.Info("http_request", "request_id", id, "method", r.Method, "path", r.URL.Path,
				"status", sw.status, "duration_ms", time.Since(start).Milliseconds())
		})
	}
}

// Recover panikleri 500'e çevirir ve süreci ayakta tutar.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "panic", rec, "stack", string(debug.Stack()))
					WriteError(w, http.StatusInternalServerError, "internal", "beklenmeyen bir hata oluştu")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
