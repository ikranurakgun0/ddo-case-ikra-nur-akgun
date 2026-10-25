// EsnafDefter veri üretici (simülatör).
//
//	simulator seed         veriyi üretir (zaten üretilmişse hiçbir şey yapmaz)
//	simulator reset        tüm iş verisini siler (süper kullanıcı gerekir)
//	simulator run          seed (gerekirse) + sürekli canlı olay akışı
//	simulator fingerprint  veri özetini (SHA-256) yazar; aynı tohum = aynı özet
//
// Ortam: DATABASE_URL, SIM_SEED (42), SIM_BUSINESSES (5), SIM_CONTACTS (3000),
// SIM_EVENTS (50000), SIM_LIVE_INTERVAL (5s; 0 = canlı akış kapalı).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"esnafdefter/internal/db"
	"esnafdefter/internal/sim"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL zorunlu")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Seed yarım kalırsa zaten baştan sıfırlanır; bu yüzden commit başına diske beklemeye (fsync) gerek yok.
	pool, err := db.Connect(ctx, url, func(c *pgxpool.Config) {
		c.ConnConfig.RuntimeParams["synchronous_commit"] = "off"
	})
	if err != nil {
		log.Error("bağlantı", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// Şemanın hazır olduğundan emin ol (api de aynısını yapar; kilit sayesinde çakışmaz).
	if err := db.Migrate(ctx, pool); err != nil {
		log.Error("migrasyon", "err", err)
		os.Exit(1)
	}

	cfg := sim.DefaultConfig()
	cfg.Seed = envInt64("SIM_SEED", cfg.Seed)
	cfg.Businesses = int(envInt64("SIM_BUSINESSES", int64(cfg.Businesses)))
	cfg.Contacts = int(envInt64("SIM_CONTACTS", int64(cfg.Contacts)))
	cfg.Events = int(envInt64("SIM_EVENTS", int64(cfg.Events)))

	switch cmd {
	case "reset":
		if err := sim.Reset(ctx, pool); err != nil {
			log.Error("reset", "err", err)
			os.Exit(1)
		}
		log.Info("veri sıfırlandı")
	case "seed":
		if err := seed(ctx, pool, cfg, log); err != nil {
			log.Error("seed", "err", err)
			os.Exit(1)
		}
	case "fingerprint":
		fp, err := sim.Fingerprint(ctx, pool)
		if err != nil {
			log.Error("fingerprint", "err", err)
			os.Exit(1)
		}
		fmt.Println(fp)
	case "run":
		if err := seed(ctx, pool, cfg, log); err != nil {
			log.Error("seed", "err", err)
			os.Exit(1)
		}
		interval, err := time.ParseDuration(envStr("SIM_LIVE_INTERVAL", "5s"))
		if err != nil || interval <= 0 {
			log.Info("canlı akış kapalı; çıkılıyor")
			return
		}
		if err := sim.RunLive(ctx, pool, interval, log); err != nil {
			log.Error("canlı akış", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "bilinmeyen komut %q (seed|reset|run|fingerprint)\n", cmd)
		os.Exit(2)
	}
}

func seed(ctx context.Context, pool *pgxpool.Pool, cfg sim.Config, log *slog.Logger) error {
	err := sim.Seed(ctx, pool, cfg, log)
	if errors.Is(err, sim.ErrAlreadySeeded) {
		log.Info("veri zaten tohumlanmış; atlandı")
		return nil
	}
	return err
}

func envInt64(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
