package db_test

import (
	"context"
	"testing"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/db"
	"esnafdefter/internal/testutil"
)

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testutil.NewPool(t) // bir kez uygulandı
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := db.Migrate(ctx, pool); err != nil {
			t.Fatalf("tekrar migrasyon başarısız: %v", err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n < 1 {
		t.Fatalf("schema_migrations: n=%d err=%v", n, err)
	}
}

func TestAuditLogIsAppendOnly(t *testing.T) {
	pool := testutil.NewPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO audit_log(action,entity,entity_id) VALUES ('x','y','1')`); err != nil {
		t.Fatalf("ekleme çalışmalı: %v", err)
	}
	for _, q := range []string{
		`UPDATE audit_log SET action='z'`,
		`DELETE FROM audit_log`,
		`TRUNCATE audit_log`,
	} {
		if _, err := pool.Exec(ctx, q); err == nil {
			t.Errorf("%q reddedilmeliydi", q)
		}
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_log`).Scan(&n)
	if n != 1 {
		t.Fatalf("kayıt korunmalıydı, n=%d", n)
	}
}

func TestUserEmailUniqueCaseInsensitive(t *testing.T) {
	pool := testutil.NewPool(t)
	b := testutil.CreateBusiness(t, pool, "Test Esnaf", "1111111111")
	testutil.CreateUser(t, pool, b, "Ali@Example.com", "parola123", auth.RoleOwner)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users(business_id,email,password_hash,role) VALUES ($1::uuid,'ali@example.COM','h','owner')`, b)
	if err == nil {
		t.Fatal("e-posta büyük/küçük harften bağımsız benzersiz olmalı")
	}
}

func TestRoleCheckConstraint(t *testing.T) {
	pool := testutil.NewPool(t)
	b := testutil.CreateBusiness(t, pool, "Test Esnaf", "2222222222")
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users(business_id,email,password_hash,role) VALUES ($1::uuid,'a@b.c','h','admin')`, b)
	if err == nil {
		t.Fatal("geçersiz rol CHECK ile reddedilmeliydi")
	}
}
