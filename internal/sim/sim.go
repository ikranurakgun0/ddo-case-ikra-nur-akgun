// Package sim deterministik veri üretici (simülatör) ve canlı olay akışıdır.
//
// Aynı tohumla (seed) çalıştırıldığında her işletme için AYNI veri üretilir: her işletmenin
// kendi rastgele sayı üreteci vardır ve yalnızca kendi verisine dokunur; bu yüzden işletmeler
// paralel çalışsa da sonuç zamanlamadan etkilenmez. Üretim, uygulamanın kendi alan paketlerini
// (fatura, ödeme, stok) kullanır; yani tüm kurallar (çift kayıt, KDV, FIFO, AOM) birebir geçerlidir.
package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/auth"
	"esnafdefter/internal/ledger"
)

// DemoPassword tüm demo hesapların parolasıdır (yalnızca demo verisi için).
const DemoPassword = "Demo123!"

var Roles = []auth.Role{auth.RoleOwner, auth.RoleAccountant, auth.RoleEmployee, auth.RoleCustomer}

// DemoEmail örn. owner@isletme1.demo
func DemoEmail(role auth.Role, business int) string {
	return fmt.Sprintf("%s@isletme%d.demo", role, business)
}

type Config struct {
	Seed       int64
	Businesses int
	Contacts   int // toplam cari sayısı
	Events     int // toplam hareket (fatura + tahsilat/ödeme + iptal/düzeltme)
	Start, End time.Time
}

func DefaultConfig() Config {
	return Config{
		Seed: 42, Businesses: 5, Contacts: 3000, Events: 50000,
		Start: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC),
	}
}

var ErrAlreadySeeded = errors.New("veri zaten tohumlanmış")

// Seed veri tabanını doldurur. Zaten tamamlanmışsa ErrAlreadySeeded döner (idempotent).
// Yarım kalmış bir önceki çalışma varsa sıfırlanıp baştan başlanır.
func Seed(ctx context.Context, pool *pgxpool.Pool, cfg Config, log *slog.Logger) error {
	var status string
	err := pool.QueryRow(ctx, `SELECT status FROM seed_state WHERE id = 1`).Scan(&status)
	switch {
	case err == nil && status == "done":
		return ErrAlreadySeeded
	case err == nil:
		log.Warn("yarım kalmış seed bulundu; sıfırlanıyor")
		if err := Reset(ctx, pool); err != nil {
			return err
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO seed_state(id, seed, status) VALUES (1, $1, 'running')
		ON CONFLICT (id) DO UPDATE SET seed = $1, status = 'running', started_at = now(), finished_at = NULL`, cfg.Seed); err != nil {
		return err
	}
	hash, err := auth.HashPassword(DemoPassword)
	if err != nil {
		return err
	}

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, cfg.Businesses)
	for i := 1; i <= cfg.Businesses; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			b := newBiz(pool, cfg, n, hash, log)
			if err := b.run(ctx); err != nil {
				errs <- fmt.Errorf("işletme %d: %w", n, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return err
	}

	// Her işletmenin ilk çeyreğini kapat: demo verisinde kapalı dönem de görünsün.
	rows, err := pool.Query(ctx, `SELECT id::text FROM businesses ORDER BY tax_no`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := ledger.ClosePeriod(ctx, pool, id, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)); err != nil {
			return err
		}
	}

	fp, err := Fingerprint(ctx, pool)
	if err != nil {
		return err
	}
	stats, err := collectStats(ctx, pool)
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `UPDATE seed_state SET status='done', finished_at=now(), fingerprint=$1, stats=$2::jsonb WHERE id=1`, fp, stats); err != nil {
		return err
	}
	log.Info("seed tamamlandı", "sure", time.Since(start).Round(time.Second).String(), "fingerprint", fp, "stats", stats)
	return nil
}

func collectStats(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var s string
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'businesses', (SELECT count(*) FROM businesses),
		'contacts', (SELECT count(*) FROM contacts),
		'products', (SELECT count(*) FROM products),
		'invoices', (SELECT count(*) FROM invoices),
		'cancelled_invoices', (SELECT count(*) FROM invoices WHERE status = 'cancelled'),
		'payments', (SELECT count(*) FROM payments),
		'deleted_payments', (SELECT count(*) FROM payments WHERE status = 'deleted'),
		'journal_entries', (SELECT count(*) FROM journal_entries),
		'stock_movements', (SELECT count(*) FROM stock_movements))::text`).Scan(&s)
	return s, err
}

// Reset tüm iş verisini siler (şema kalır). Değiştirilemez tablolardaki tetikleyicileri atlamak
// için session_replication_role=replica gerekir; bu da süper kullanıcı yetkisi ister.
// Bu yalnızca demo/geliştirme ortamında kullanılır; uygulamanın hiçbir HTTP ucu bunu çağırmaz.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		return fmt.Errorf("reset için süper kullanıcı yetkisi gerekli: %w", err)
	}
	if _, err := tx.Exec(ctx, `TRUNCATE audit_log, idempotency_keys, stock_movements, payment_allocations, payments,
		invoice_lines, invoices, invoice_counters, journal_lines, journal_entries, period_locks, business_settings,
		products, accounts, refresh_tokens, users, contacts, businesses, seed_state RESTART IDENTITY CASCADE`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Fingerprint içerik üzerinden (kimlik/zaman damgası hariç) SHA-256 özeti üretir.
// İki seed çalıştırması aynı tohumla aynı özeti vermelidir.
func Fingerprint(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) (string, error) {
	return fingerprint(ctx, q)
}
