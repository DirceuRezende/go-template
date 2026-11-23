package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dirceurezende/user-template-go/internal/user/security"
)

type config struct {
	Env            string // "dev" liga o segredo JWT de dev e o seed do admin
	HTTPAddr       string
	DatabaseURL    string
	KafkaBrokers   []string
	JWTSecret      []byte
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	OutboxInterval time.Duration
}

// Só para desenvolvimento local (APP_ENV=dev).
const (
	devJWTSecret     = "dev-only-secret-change-me-0123456789abcdef"
	devAdminEmail    = "admin@example.com"
	devAdminPassword = "admin12345"
)

// loadConfig lê o ambiente. Defaults = serviços do docker-compose.yml.
func loadConfig() (config, error) {
	var errs []error
	duration := func(key, def string) time.Duration {
		d, err := time.ParseDuration(env(key, def))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return d
	}
	cfg := config{
		Env:            env("APP_ENV", ""),
		HTTPAddr:       env("HTTP_ADDR", ":8080"),
		DatabaseURL:    env("DATABASE_URL", "postgres://users:users@localhost:5432/users?sslmode=disable"),
		KafkaBrokers:   strings.Split(env("KAFKA_BOOTSTRAP_SERVERS", "localhost:9092"), ","),
		AccessTTL:      duration("ACCESS_TTL", "15m"),
		RefreshTTL:     duration("REFRESH_TTL", "168h"),
		OutboxInterval: duration("OUTBOX_POLL_INTERVAL", "1s"),
	}
	secret := os.Getenv("JWT_SECRET")
	if secret == "" && cfg.Env == "dev" {
		secret = devJWTSecret
	}
	if len(secret) < security.MinSecretBytes {
		errs = append(errs, errors.New("JWT_SECRET é obrigatório e precisa de ao menos 32 bytes"))
	}
	cfg.JWTSecret = []byte(secret)
	return cfg, errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
