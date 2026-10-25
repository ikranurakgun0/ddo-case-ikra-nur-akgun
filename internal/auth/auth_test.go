package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("gizli-parola")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("argon2id bekleniyordu: %s", h)
	}
	if ok, err := VerifyPassword("gizli-parola", h); !ok || err != nil {
		t.Fatalf("doğru parola kabul edilmeliydi: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword("yanlis", h); ok {
		t.Fatal("yanlış parola kabul edilmemeliydi")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	a, _ := HashPassword("aynı")
	b, _ := HashPassword("aynı")
	if a == b {
		t.Fatal("aynı parola farklı tuzla farklı özet üretmeliydi")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	for _, h := range []string{"", "düz-metin", "$bcrypt$x$y$z$w", "$argon2id$v=19$m=1,t=1,p=1$!!$!!"} {
		if ok, err := VerifyPassword("x", h); ok || err == nil {
			t.Fatalf("%q reddedilmeliydi (ok=%v err=%v)", h, ok, err)
		}
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	iss := NewTokenIssuer("0123456789abcdef0123456789abcdef", time.Minute)
	tok, err := iss.IssueAccess("u1", "b1", RoleAccountant)
	if err != nil {
		t.Fatal(err)
	}
	c, err := iss.ParseAccess(tok)
	if err != nil || c.UserID != "u1" || c.BusinessID != "b1" || c.Role != RoleAccountant {
		t.Fatalf("beklenmeyen sonuç: %+v %v", c, err)
	}
}

func TestAccessTokenExpires(t *testing.T) {
	now := time.Now()
	iss := NewTokenIssuer("0123456789abcdef0123456789abcdef", time.Minute).WithClock(func() time.Time { return now })
	tok, _ := iss.IssueAccess("u1", "b1", RoleOwner)
	later := iss.WithClock(func() time.Time { return now.Add(2 * time.Minute) })
	if _, err := later.ParseAccess(tok); err == nil {
		t.Fatal("süresi dolan belirteç reddedilmeliydi")
	}
}

func TestAccessTokenWrongSecretAndTamper(t *testing.T) {
	a := NewTokenIssuer("0123456789abcdef0123456789abcdef", time.Minute)
	b := NewTokenIssuer("ffffffffffffffffffffffffffffffff", time.Minute)
	tok, _ := a.IssueAccess("u1", "b1", RoleOwner)
	if _, err := b.ParseAccess(tok); err == nil {
		t.Fatal("başka sırla imzalı belirteç reddedilmeliydi")
	}
	if _, err := a.ParseAccess(tok[:len(tok)-2] + "xx"); err == nil {
		t.Fatal("kurcalanmış belirteç reddedilmeliydi")
	}
	if _, err := a.ParseAccess("a.b.c"); err == nil {
		t.Fatal("çöp belirteç reddedilmeliydi")
	}
}

func TestRefreshTokenHashing(t *testing.T) {
	p1, h1, _ := NewRefreshToken()
	p2, _, _ := NewRefreshToken()
	if p1 == p2 {
		t.Fatal("belirteçler benzersiz olmalı")
	}
	if string(HashRefreshToken(p1)) != string(h1) {
		t.Fatal("özet tutarsız")
	}
}
