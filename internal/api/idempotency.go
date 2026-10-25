package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"esnafdefter/internal/httpx"
)

// runIdempotent iş fonksiyonunu tek bir transaction içinde çalıştırır ve sonucu 201 ile döner.
//
// Idempotency-Key başlığı varsa: aynı anahtar + aynı istek ikinci kez geldiğinde iş tekrar
// çalışmaz, ilk yanıt aynen döner. Aynı anahtar farklı bir istekle kullanılırsa 422 döner.
// Eşzamanlı iki istek aynı anahtarla gelirse ikincisi, birincinin commit'ini bekler
// (birincil anahtar çakışması) ve ardından ilk yanıtı alır. Başarısız istekler anahtarı yakmaz:
// transaction geri alındığı için anahtar kaydı da geri alınır.
func (s *Server) runIdempotent(w http.ResponseWriter, r *http.Request, body []byte, businessID string,
	work func(ctx context.Context, tx pgx.Tx) (any, error)) {

	key := r.Header.Get("Idempotency-Key")
	if len(key) > 200 {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "Idempotency-Key en fazla 200 karakter")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internalError(w, "transaction", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	if key != "" {
		hash := sha256.Sum256(append([]byte(r.Method+" "+r.URL.Path+"\n"), body...))
		tag, err := tx.Exec(r.Context(),
			`INSERT INTO idempotency_keys(business_id, key, request_hash) VALUES ($1::uuid, $2, $3)
			 ON CONFLICT (business_id, key) DO NOTHING`, businessID, key, hash[:])
		if err != nil {
			s.internalError(w, "idempotans", err)
			return
		}
		if tag.RowsAffected() == 0 {
			var storedHash, resp []byte
			var status *int
			err := tx.QueryRow(r.Context(),
				`SELECT request_hash, status_code, response_body FROM idempotency_keys WHERE business_id = $1::uuid AND key = $2`,
				businessID, key).Scan(&storedHash, &status, &resp)
			if err != nil || status == nil {
				httpx.WriteError(w, http.StatusConflict, "idempotency_in_progress", "aynı anahtarlı istek hâlâ işleniyor; tekrar deneyin")
				return
			}
			if !bytes.Equal(storedHash, hash[:]) {
				httpx.WriteError(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "bu anahtar farklı bir istekle daha önce kullanılmış")
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Idempotent-Replay", "true")
			w.WriteHeader(*status)
			_, _ = w.Write(resp)
			return
		}
	}

	result, err := work(r.Context(), tx)
	if err != nil {
		s.writeDomainError(w, err)
		return
	}
	respBytes, _ := json.Marshal(result)
	if key != "" {
		if _, err := tx.Exec(r.Context(),
			`UPDATE idempotency_keys SET status_code = $3, response_body = $4::jsonb WHERE business_id = $1::uuid AND key = $2`,
			businessID, key, http.StatusCreated, respBytes); err != nil {
			s.internalError(w, "idempotans", err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		s.writeDomainError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write(respBytes)
}
