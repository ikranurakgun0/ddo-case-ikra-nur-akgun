// Package testutil gerçek PostgreSQL ile entegrasyon testleri için yardımcılar sağlar.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/db"
)

// NewPool her test için yepyeni, migrasyonları uygulanmış izole bir veritabanı açar.
// TEST_DATABASE_URL tanımlı değilse test atlanır (make test bunu docker ile sağlar).
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL tanımlı değil; entegrasyon testi atlandı")
	}
	ctx := context.Background()
	ap, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatalf("admin bağlantısı: %v", err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "t_" + hex.EncodeToString(b)
	if _, err := ap.Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("veritabanı oluşturulamadı: %v", err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("test havuzu: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrasyon: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = ap.Exec(c, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		ap.Close()
	})
	return pool
}

type User struct {
	ID, BusinessID, Email, Password string
	Role                            auth.Role
}

// CreateBusiness bir işletme ekler ve kimliğini döner.
func CreateBusiness(t testing.TB, pool *pgxpool.Pool, name, taxNo string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO businesses(name, tax_no) VALUES ($1,$2) RETURNING id::text`, name, taxNo).Scan(&id); err != nil {
		t.Fatalf("işletme: %v", err)
	}
	return id
}

// CreateUser verilen işletmeye bir kullanıcı ekler.
func CreateUser(t testing.TB, pool *pgxpool.Pool, businessID, email, password string, role auth.Role) User {
	t.Helper()
	h, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := User{BusinessID: businessID, Email: email, Password: password, Role: role}
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users(business_id,email,password_hash,role) VALUES ($1::uuid,$2,$3,$4) RETURNING id::text`,
		businessID, email, h, string(role)).Scan(&u.ID); err != nil {
		t.Fatalf("kullanıcı: %v", err)
	}
	return u
}

// CreateContact bir cari ekler ve kimliğini döner.
func CreateContact(t testing.TB, pool *pgxpool.Pool, businessID, kind, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO contacts(business_id, kind, name) VALUES ($1::uuid,$2,$3) RETURNING id::text`,
		businessID, kind, name).Scan(&id); err != nil {
		t.Fatalf("cari: %v", err)
	}
	return id
}
