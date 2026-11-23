// Comando api sobe HTTP, relay do outbox e consumer Kafka. Todo o wiring fica aqui.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"

	"github.com/dirceurezende/user-template-go/internal/user"
	"github.com/dirceurezende/user-template-go/internal/user/httpapi"
	"github.com/dirceurezende/user-template-go/internal/user/kafka"
	"github.com/dirceurezende/user-template-go/internal/user/notify"
	"github.com/dirceurezende/user-template-go/internal/user/postgres"
	"github.com/dirceurezende/user-template-go/internal/user/security"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("encerrando com erro", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if err := loadDotEnv(".env"); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	shutdownOTel, err := setupOTel(ctx)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownOTel(shutdownCtx) // descarrega o que ficou no buffer
	}()
	if cfg.Env != "dev" {
		gin.SetMode(gin.ReleaseMode)
	}
	a, err := newApp(ctx, cfg, notify.Log{})
	if err != nil {
		return err
	}
	defer a.close()
	if cfg.Env == "dev" {
		if err := seedAdmin(ctx, a.service); err != nil {
			return err
		}
	}

	var wg sync.WaitGroup
	a.start(ctx, &wg)
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: a.handler, ReadHeaderTimeout: 5 * time.Second}
	wg.Go(func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})
	slog.Info("ouvindo", "addr", cfg.HTTPAddr)
	err = srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	wg.Wait()
	return err
}

type app struct {
	pool     *pgxpool.Pool
	producer *kgo.Client
	consumer *kgo.Client
	service  *user.Service
	handler  http.Handler
	relay    postgres.Relay
	listener kafka.Consumer
}

// newApp conecta na infra, aplica migrations, cria tópicos e monta o grafo de dependências.
func newApp(ctx context.Context, cfg config, notifier user.Notifier) (_ *app, err error) {
	a := &app{}
	defer func() {
		if err != nil {
			a.close()
		}
	}()
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	// shortcut: o polling do relay gera um trace raiz por SELECT do outbox (1/s), upgrade: sampler que descarte esses spans.
	poolCfg.ConnConfig.Tracer = otelpgx.NewTracer()
	if a.pool, err = pgxpool.NewWithConfig(ctx, poolCfg); err != nil {
		return nil, err
	}
	if err = otelpgx.RecordStats(a.pool); err != nil {
		return nil, err
	}
	if err = postgres.Migrate(ctx, a.pool); err != nil {
		return nil, err
	}
	tracer := kotel.NewTracer()
	hooks := kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(tracer), kotel.WithMeter(kotel.NewMeter())).Hooks()...)
	if a.producer, err = kgo.NewClient(kgo.SeedBrokers(cfg.KafkaBrokers...), hooks); err != nil {
		return nil, err
	}
	if err = kafka.EnsureTopics(ctx, a.producer); err != nil {
		return nil, err
	}
	opts := append(kafka.ConsumerOpts("user-template"), kgo.SeedBrokers(cfg.KafkaBrokers...), hooks)
	if a.consumer, err = kgo.NewClient(opts...); err != nil {
		return nil, err
	}

	store := postgres.NewStore(a.pool)
	jwt := security.JWT{Secret: cfg.JWTSecret, TTL: cfg.AccessTTL}
	a.service = &user.Service{
		Store: store, Notifier: notifier, Hasher: security.Bcrypt{},
		AccessTokens: jwt, RefreshTTL: cfg.RefreshTTL,
	}
	a.handler = httpapi.New(a.service, jwt)
	a.relay = postgres.Relay{Pool: a.pool, Producer: kafka.Producer{Client: a.producer}, Interval: cfg.OutboxInterval}
	a.listener = kafka.Consumer{Client: a.consumer, Service: a.service, Tracer: tracer}
	return a, nil
}

// start roda relay e consumer em goroutines até o ctx ser cancelado.
func (a *app) start(ctx context.Context, wg *sync.WaitGroup) {
	wg.Go(func() { a.relay.Run(ctx) })
	wg.Go(func() { a.listener.Run(ctx) })
}

func (a *app) close() {
	if a.consumer != nil {
		a.consumer.Close() // commita os offsets marcados e sai do grupo
	}
	if a.producer != nil {
		a.producer.Close()
	}
	if a.pool != nil {
		a.pool.Close()
	}
}

// seedAdmin cria o admin de dev se ainda não existe. Em produção o primeiro admin é provisionado por fora.
func seedAdmin(ctx context.Context, svc *user.Service) error {
	_, err := svc.Store.UserByEmail(ctx, devAdminEmail)
	if !errors.Is(err, user.ErrNotFound) {
		return err
	}
	hash, err := svc.Hasher.Hash(devAdminPassword)
	if err != nil {
		return err
	}
	admin, err := user.New(devAdminEmail, "Admin", hash, time.Now())
	if err != nil {
		return err
	}
	admin.Grant(user.RoleAdmin)
	return svc.Store.CreateUser(ctx, admin)
}
