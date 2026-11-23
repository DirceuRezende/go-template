package main

import (
	"bufio"
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

// loadDotEnv aplica KEY=VALUE de path no ambiente; variável já definida no ambiente vence.
// Arquivo ausente não é erro (produção usa o ambiente de verdade).
// shortcut: sem interpolação ${VAR} nem valor multilinha, upgrade: github.com/joho/godotenv.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // só leitura
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return fmt.Errorf("%s:%d: esperado KEY=VALUE", path, n)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			if err := os.Setenv(key, val); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}
