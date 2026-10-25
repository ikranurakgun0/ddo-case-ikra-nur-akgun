package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/api"
	"esnafdefter/internal/auth"
	"esnafdefter/internal/testutil"
)

type env struct {
	srv  *httptest.Server
	pool *pgxpool.Pool
	user testutil.User
}

func setup(t *testing.T) *env {
	t.Helper()
	pool := testutil.NewPool(t)
	biz := testutil.CreateBusiness(t, pool, "Deneme Market", "3333333333")
	u := testutil.CreateUser(t, pool, biz, "sahip@deneme.test", "dogru-parola", auth.RoleOwner)
	iss := auth.NewTokenIssuer("0123456789abcdef0123456789abcdef", 15*time.Minute)
	s, err := api.NewServer(pool, slog.New(slog.NewJSONHandler(io.Discard, nil)), iss, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Routes())
	t.Cleanup(ts.Close)
	return &env{srv: ts, pool: pool, user: u}
}

func (e *env) post(t *testing.T, path string, body any, bearer string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return e.do(t, req)
}

func (e *env) get(t *testing.T, path, bearer string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return e.do(t, req)
}

func (e *env) do(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func (e *env) login(t *testing.T) map[string]any {
	code, m := e.post(t, "/auth/login", map[string]string{"email": e.user.Email, "password": e.user.Password}, "")
	if code != 200 {
		t.Fatalf("giriş başarısız: %d %v", code, m)
	}
	return m
}

func TestHealthReportsDatabase(t *testing.T) {
	e := setup(t)
	code, m := e.get(t, "/health", "")
	if code != 200 || m["status"] != "ok" || m["db"] != "up" {
		t.Fatalf("sağlık: %d %v", code, m)
	}
}

func TestHealthIs503WhenDatabaseDown(t *testing.T) {
	e := setup(t)
	e.pool.Close()
	code, m := e.get(t, "/health", "")
	if code != 503 || m["db"] != "down" {
		t.Fatalf("503 bekleniyordu: %d %v", code, m)
	}
}

func TestLoginSuccessAndMe(t *testing.T) {
	e := setup(t)
	m := e.login(t)
	if m["token_type"] != "Bearer" || m["role"] != "owner" {
		t.Fatalf("beklenmeyen yanıt: %v", m)
	}
	code, me := e.get(t, "/me", m["access_token"].(string))
	if code != 200 || me["user_id"] != e.user.ID || me["role"] != "owner" {
		t.Fatalf("/me: %d %v", code, me)
	}
}

func TestLoginWrongPasswordAndUnknownUserLookIdentical(t *testing.T) {
	e := setup(t)
	c1, m1 := e.post(t, "/auth/login", map[string]string{"email": e.user.Email, "password": "yanlis"}, "")
	c2, m2 := e.post(t, "/auth/login", map[string]string{"email": "yok@deneme.test", "password": "yanlis"}, "")
	if c1 != 401 || c2 != 401 || errCode(m1) != "invalid_credentials" || errCode(m2) != "invalid_credentials" {
		t.Fatalf("her ikisi de 401/invalid_credentials olmalı: %d %v | %d %v", c1, m1, c2, m2)
	}
}

func TestLoginValidation(t *testing.T) {
	e := setup(t)
	code, m := e.post(t, "/auth/login", map[string]string{"email": "", "password": ""}, "")
	if code != 400 || errCode(m) != "validation_error" {
		t.Fatalf("400 bekleniyordu: %d %v", code, m)
	}
}

func TestInactiveUserCannotLogin(t *testing.T) {
	e := setup(t)
	if _, err := e.pool.Exec(context.Background(), `UPDATE users SET active=false WHERE id=$1::uuid`, e.user.ID); err != nil {
		t.Fatal(err)
	}
	code, _ := e.post(t, "/auth/login", map[string]string{"email": e.user.Email, "password": e.user.Password}, "")
	if code != 401 {
		t.Fatalf("pasif kullanıcı 401 almalı: %d", code)
	}
}

func TestRefreshRotatesAndOldTokenIsRejected(t *testing.T) {
	e := setup(t)
	first := e.login(t)
	code, second := e.post(t, "/auth/refresh", map[string]string{"refresh_token": first["refresh_token"].(string)}, "")
	if code != 200 || second["refresh_token"] == first["refresh_token"] {
		t.Fatalf("yenileme çalışmalı ve yeni belirteç vermeli: %d %v", code, second)
	}
	code, _ = e.post(t, "/auth/refresh", map[string]string{"refresh_token": first["refresh_token"].(string)}, "")
	if code != 401 {
		t.Fatalf("eski belirteç reddedilmeli: %d", code)
	}
}

func TestRefreshReuseRevokesAllSessions(t *testing.T) {
	e := setup(t)
	first := e.login(t)
	_, second := e.post(t, "/auth/refresh", map[string]string{"refresh_token": first["refresh_token"].(string)}, "")
	// Eski belirteç tekrar sunuldu -> çalıntı sayılır, yeni belirteç de iptal olmalı.
	e.post(t, "/auth/refresh", map[string]string{"refresh_token": first["refresh_token"].(string)}, "")
	code, _ := e.post(t, "/auth/refresh", map[string]string{"refresh_token": second["refresh_token"].(string)}, "")
	if code != 401 {
		t.Fatalf("yeniden kullanım tespitinden sonra tüm oturumlar kapanmalı: %d", code)
	}
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	e := setup(t)
	m := e.login(t)
	if code, _ := e.post(t, "/auth/logout", map[string]string{"refresh_token": m["refresh_token"].(string)}, ""); code != 204 {
		t.Fatalf("204 bekleniyordu: %d", code)
	}
	if code, _ := e.post(t, "/auth/refresh", map[string]string{"refresh_token": m["refresh_token"].(string)}, ""); code != 401 {
		t.Fatalf("çıkıştan sonra yenileme 401 olmalı: %d", code)
	}
}

func TestProtectedRouteAndUnknownRoute(t *testing.T) {
	e := setup(t)
	if code, m := e.get(t, "/me", ""); code != 401 || errCode(m) != "unauthenticated" {
		t.Fatalf("/me belirteçsiz 401 olmalı: %d %v", code, m)
	}
	if code, m := e.get(t, "/yok", ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("404 JSON hata biçimi bekleniyordu: %d %v", code, m)
	}
}

func TestLoginWritesAuditLog(t *testing.T) {
	e := setup(t)
	e.login(t)
	var n int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='login' AND actor_id=$1::uuid`, e.user.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("denetim kaydında 1 giriş bekleniyordu: %d", n)
	}
}
