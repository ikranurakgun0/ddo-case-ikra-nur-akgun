package sim_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/sim"
	"esnafdefter/internal/testutil"
)

var quiet = slog.New(slog.NewJSONHandler(io.Discard, nil))

func small(seed int64) sim.Config {
	c := sim.DefaultConfig()
	c.Seed, c.Businesses, c.Contacts, c.Events = seed, 2, 80, 600
	return c
}

func seedDB(t *testing.T, cfg sim.Config) (*pgxpool.Pool, string) {
	t.Helper()
	pool := testutil.NewPool(t)
	if err := sim.Seed(context.Background(), pool, cfg, quiet); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var fp string
	if err := pool.QueryRow(context.Background(), `SELECT fingerprint FROM seed_state WHERE id=1`).Scan(&fp); err != nil {
		t.Fatal(err)
	}
	return pool, fp
}

func TestSameSeedProducesIdenticalData(t *testing.T) {
	_, a := seedDB(t, small(7))
	_, b := seedDB(t, small(7))
	_, c := seedDB(t, small(8))
	if a == "" || a != b {
		t.Fatalf("aynı tohum aynı özeti vermeli:\n%s\n%s", a, b)
	}
	if a == c {
		t.Fatal("farklı tohum farklı veri üretmeli")
	}
}

func TestSeedIsIdempotentAndResettable(t *testing.T) {
	cfg := small(3)
	pool, fp := seedDB(t, cfg)
	ctx := context.Background()
	if err := sim.Seed(ctx, pool, cfg, quiet); err != sim.ErrAlreadySeeded {
		t.Fatalf("ikinci seed ErrAlreadySeeded dönmeli: %v", err)
	}
	if err := sim.Reset(ctx, pool); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM businesses`).Scan(&n)
	if n != 0 {
		t.Fatalf("reset sonrası veri kalmamalı: %d", n)
	}
	if err := sim.Seed(ctx, pool, cfg, quiet); err != nil {
		t.Fatal(err)
	}
	var fp2 string
	_ = pool.QueryRow(ctx, `SELECT fingerprint FROM seed_state WHERE id=1`).Scan(&fp2)
	if fp2 != fp {
		t.Fatalf("reset + seed ilk seed ile aynı veriyi vermeli:\n%s\n%s", fp, fp2)
	}
}

// Üretilen verinin tamamı için muhasebe değişmezleri.
func TestSeededDataInvariants(t *testing.T) {
	pool, _ := seedDB(t, small(11))
	ctx := context.Background()
	check := func(name, sql string, want int64) {
		var got int64
		if err := pool.QueryRow(ctx, sql).Scan(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: %d bekleniyordu, %d geldi", name, want, got)
		}
	}
	check("toplam borç = toplam alacak", `SELECT coalesce(sum(debit),0)-coalesce(sum(credit),0) FROM journal_lines`, 0)
	check("dengesiz yevmiye kaydı", `SELECT count(*) FROM (SELECT entry_id FROM journal_lines GROUP BY entry_id HAVING sum(debit)<>sum(credit)) x`, 0)
	check("fazla mahsup edilmiş fatura", `SELECT count(*) FROM invoices i WHERE
		(SELECT coalesce(sum(amount),0) FROM payment_allocations a WHERE a.invoice_id=i.id AND a.reversed_at IS NULL) > i.gross_total`, 0)
	check("fazla mahsup edilmiş ödeme", `SELECT count(*) FROM payments p WHERE
		(SELECT coalesce(sum(amount),0) FROM payment_allocations a WHERE a.payment_id=p.id AND a.reversed_at IS NULL) > p.amount`, 0)
	check("iptal edilmiş faturada aktif mahsup", `SELECT count(*) FROM payment_allocations a JOIN invoices i ON i.id=a.invoice_id
		WHERE a.reversed_at IS NULL AND i.status='cancelled'`, 0)
	check("fatura numarası boşluğu", `SELECT count(*) FROM (SELECT business_id, series, year, max(number)-count(*) AS gap
		FROM invoices GROUP BY business_id, series, year HAVING max(number) <> count(*)) x`, 0)
	check("negatif stok", `SELECT count(*) FROM (SELECT product_id FROM stock_movements WHERE active GROUP BY product_id
		HAVING sum(CASE direction WHEN 'in' THEN quantity_milli ELSE -quantity_milli END) < 0) x`, 0)
	// 153 hesabı = stok değeri (işletme bazında)
	check("153 ≠ stok değeri olan işletme", `SELECT count(*) FROM (
		SELECT a.business_id, sum(l.debit)-sum(l.credit) AS bal FROM accounts a JOIN journal_lines l ON l.account_id=a.id WHERE a.code='153' GROUP BY a.business_id) x
		JOIN (SELECT business_id, sum(CASE direction WHEN 'in' THEN value ELSE -value END) AS v FROM stock_movements WHERE active GROUP BY business_id) s
		ON s.business_id = x.business_id WHERE x.bal <> s.v`, 0)
	check("621 ≠ çıkış maliyeti olan işletme", `SELECT count(*) FROM (
		SELECT a.business_id, sum(l.debit)-sum(l.credit) AS bal FROM accounts a JOIN journal_lines l ON l.account_id=a.id WHERE a.code='621' GROUP BY a.business_id) x
		JOIN (SELECT business_id, sum(value) AS v FROM stock_movements WHERE active AND direction='out' GROUP BY business_id) s
		ON s.business_id = x.business_id WHERE x.bal <> s.v`, 0)
	// Demo hesapları: her işletmede her rol için bir kullanıcı
	check("demo kullanıcı sayısı", `SELECT count(*) FROM users`, 8)
	check("demo müşteri bir cariye bağlı", `SELECT count(*) FROM users WHERE role='customer' AND contact_id IS NOT NULL`, 2)
	check("kapalı dönem", `SELECT count(*) FROM period_locks WHERE closed_through = '2026-03-31'`, 2)
	// Çeşitlilik: simülasyon gerçekten iptal/silme/düzeltme senaryoları üretmiş olmalı
	for name, sql := range map[string]string{
		"iptal edilmiş fatura":   `SELECT count(*) FROM invoices WHERE status='cancelled'`,
		"silinmiş ödeme":         `SELECT count(*) FROM payments WHERE status='deleted'`,
		"avans":                  `SELECT count(*) FROM journal_lines l JOIN accounts a ON a.id=l.account_id WHERE a.code='340'`,
		"maliyet yeniden hesabı": `SELECT count(*) FROM audit_log WHERE action='stock_cost_recalc'`,
		"kısmi mahsup":           `SELECT count(*) FROM invoices i WHERE i.status='issued' AND (SELECT coalesce(sum(amount),0) FROM payment_allocations a WHERE a.invoice_id=i.id AND a.reversed_at IS NULL) BETWEEN 1 AND i.gross_total-1`,
	} {
		var n int
		if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil || n == 0 {
			t.Errorf("simülasyon %q senaryosu üretmeli (n=%d err=%v)", name, n, err)
		}
	}
}
