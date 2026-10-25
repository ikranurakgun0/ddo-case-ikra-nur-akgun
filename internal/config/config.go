// Package config ortam değişkenlerinden yapılandırmayı okur (12-factor).
package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type Config struct {
	Addr        string
	DatabaseURL string
	JWTSecret   string
	AccessTTL   time.Duration
	RefreshTTL  time.Duration
	LogLevel    string
}

// Load ortam değişkenlerini okur ve doğrular.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom test edilebilirlik için getenv fonksiyonunu dışarıdan alır.
func LoadFrom(getenv func(string) string) (Config, error) {
	c := Config{
		Addr:        def(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		JWTSecret:   getenv("JWT_SECRET"),
		LogLevel:    def(getenv("LOG_LEVEL"), "info"),
	}
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL zorunlu"))
	}
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET en az 32 karakter olmalı"))
	}
	var err error
	if c.AccessTTL, err = dur(getenv("ACCESS_TTL"), 15*time.Minute); err != nil {
		errs = append(errs, fmt.Errorf("ACCESS_TTL: %w", err))
	}
	if c.RefreshTTL, err = dur(getenv("REFRESH_TTL"), 168*time.Hour); err != nil {
		errs = append(errs, fmt.Errorf("REFRESH_TTL: %w", err))
	}
	return c, errors.Join(errs...)
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func dur(v string, d time.Duration) (time.Duration, error) {
	if v == "" {
		return d, nil
	}
	x, err := time.ParseDuration(v)
	if err != nil {
		return 0, err
	}
	if x <= 0 {
		return 0, errors.New("pozitif olmalı")
	}
	return x, nil
}
