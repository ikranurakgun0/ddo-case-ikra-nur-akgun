package api

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"esnafdefter/internal/httpx"
)

// Sayfalama imleci (cursor) opaktır: istemci next_after değerini olduğu gibi geri gönderir.
// İçerik: sıralama anahtarı (ad, SKU ya da oluşturulma zamanı) ve kaydın id'si.
func encodeCursor(key, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key + "\x1f" + id))
}

func decodeCursor(c string) (key, id string, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(b), "\x1f", 2)
	if len(parts) != 2 || !isUUID(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// parseCursor boş imleci kabul eder; geçersizse 400 yazar.
func parseCursor(w http.ResponseWriter, after string) (key, id string, ok bool) {
	if after == "" {
		return "", "", true
	}
	key, id, ok = decodeCursor(after)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "validation_error", "after geçersiz")
	}
	return key, id, ok
}

func timeCursor(t time.Time, id string) string {
	return encodeCursor(t.UTC().Format(time.RFC3339Nano), id)
}

// oneOf değer izin verilenlerden biri ya da boşsa true döner.
func oneOf(v string, allowed ...string) bool {
	if v == "" {
		return true
	}
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
