package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

const goodSecret = "0123456789abcdef0123456789abcdef"

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"DATABASE_URL": "postgres://x", "JWT_SECRET": goodSecret}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.AccessTTL != 15*time.Minute || c.RefreshTTL != 168*time.Hour {
		t.Fatalf("varsayılanlar yanlış: %+v", c)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{"JWT_SECRET": goodSecret}))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("DATABASE_URL hatası bekleniyordu: %v", err)
	}
}

func TestLoadRejectsShortSecret(t *testing.T) {
	_, err := LoadFrom(env(map[string]string{"DATABASE_URL": "x", "JWT_SECRET": "kisa"}))
	if err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("JWT_SECRET hatası bekleniyordu: %v", err)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	for _, v := range []string{"abc", "-5m", "0s"} {
		_, err := LoadFrom(env(map[string]string{"DATABASE_URL": "x", "JWT_SECRET": goodSecret, "ACCESS_TTL": v}))
		if err == nil {
			t.Fatalf("%q reddedilmeliydi", v)
		}
	}
}
