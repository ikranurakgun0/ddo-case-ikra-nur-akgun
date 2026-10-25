package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"esnafdefter/internal/auth"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

func guarded(iss *auth.TokenIssuer, roles ...auth.Role) http.Handler {
	return Authenticate(iss)(RequireRole(roles...)(okHandler))
}

func do(h http.Handler, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthorizationMatrix(t *testing.T) {
	iss := auth.NewTokenIssuer("0123456789abcdef0123456789abcdef", time.Minute)
	h := guarded(iss, auth.RoleOwner, auth.RoleAccountant)

	tokFor := func(r auth.Role) string { s, _ := iss.IssueAccess("u", "b", r); return s }
	cases := []struct {
		name   string
		bearer string
		want   int
	}{
		{"belirteç yok", "", 401},
		{"çöp belirteç", "abc", 401},
		{"müşteri yasak", tokFor(auth.RoleCustomer), 403},
		{"çalışan yasak", tokFor(auth.RoleEmployee), 403},
		{"muhasebeci izinli", tokFor(auth.RoleAccountant), 200},
		{"sahip izinli", tokFor(auth.RoleOwner), 200},
	}
	for _, c := range cases {
		if got := do(h, c.bearer).Code; got != c.want {
			t.Errorf("%s: %d bekleniyordu, %d geldi", c.name, c.want, got)
		}
	}
}

func TestRequireRoleWithoutAuthenticateIs401(t *testing.T) {
	rec := do(RequireRole(auth.RoleOwner)(okHandler), "")
	if rec.Code != 401 {
		t.Fatalf("kimlik yokken 401 bekleniyordu, %d geldi", rec.Code)
	}
}

func TestErrorFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 418, "demlik", "ben bir demlikim")
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 418 || body.Error.Code != "demlik" || body.Error.Message == "" {
		t.Fatalf("hata biçimi yanlış: %s", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type yanlış: %s", ct)
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	var dst struct{ A string }
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"x","b":1}`))
	rec := httptest.NewRecorder()
	if DecodeJSON(rec, req, &dst) || rec.Code != 400 {
		t.Fatalf("bilinmeyen alan reddedilmeliydi (code=%d)", rec.Code)
	}
}
