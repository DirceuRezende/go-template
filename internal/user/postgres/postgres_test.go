package postgres_test

import (
	"context"
	"errors"
	"flag"
	"os"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/dirceurezende/user-template-go/internal/user"
	"github.com/dirceurezende/user-template-go/internal/user/postgres"
)

var pool *pgxpool.Pool

// TestMain sobe um Postgres (Testcontainers) para o pacote todo.
func TestMain(m *testing.M) {
	flag.Parse()
	if testing.Short() {
		os.Exit(0) // make test-short: sem containers
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.BasicWaitStrategies())
	if err != nil {
		panic(err)
	}
	dsn := container.MustConnectionString(ctx, "sslmode=disable")
	if pool, err = pgxpool.New(ctx, dsn); err != nil {
		panic(err)
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		panic(err)
	}
	code := m.Run()
	pool.Close()
	_ = container.Terminate(ctx)
	os.Exit(code)
}

func reset(t *testing.T) *postgres.Store {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE users, user_roles, refresh_tokens, outbox`); err != nil {
		t.Fatal(err)
	}
	return postgres.NewStore(pool)
}

func newUser(t *testing.T, email string, at time.Time) user.User {
	t.Helper()
	u, err := user.New(email, "Ana", "hash", at)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUserRoundTrip(t *testing.T) {
	ctx, store := context.Background(), reset(t)
	u := newUser(t, "ana@example.com", time.Now().UTC().Truncate(time.Microsecond))
	u.Grant(user.RoleAdmin)
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(ctx, newUser(t, "ana@example.com", time.Now())); !errors.Is(err, user.ErrEmailTaken) {
		t.Fatalf("email repetido: err = %v", err)
	}

	got, err := store.UserByEmail(ctx, "ana@example.com")
	if err != nil || got.ID != u.ID || !got.CreatedAt.Equal(u.CreatedAt) || len(got.Roles) != 2 || got.Roles[0] != user.RoleUser {
		t.Fatalf("lido: %+v, %v", got, err)
	}

	got.Deactivate()
	_ = got.Rename("Bia")
	if err := store.SaveUser(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, _ := store.UserByID(ctx, u.ID)
	if again.Active || again.Name != "Bia" {
		t.Fatalf("após SaveUser: %+v", again)
	}
	if _, err := store.UserByID(ctx, uuid.New()); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("inexistente: err = %v", err)
	}
}

func TestListUsersOrdersByCreation(t *testing.T) {
	ctx, store := context.Background(), reset(t)
	base := time.Now()
	for i, e := range []string{"c@x.com", "a@x.com", "b@x.com"} {
		if err := store.CreateUser(ctx, newUser(t, e, base.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	users, total, err := store.ListUsers(ctx, 1, 2)
	if err != nil || total != 3 || len(users) != 2 || users[0].Email != "a@x.com" || users[1].Email != "b@x.com" {
		t.Fatalf("lista: %+v total=%d err=%v", users, total, err)
	}
}

func TestRefreshTokens(t *testing.T) {
	ctx, store := context.Background(), reset(t)
	u := newUser(t, "ana@example.com", time.Now())
	_ = store.CreateUser(ctx, u)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	for _, h := range []string{"h1", "h2", "h3"} {
		if err := store.SaveRefreshToken(ctx, user.RefreshToken{Hash: h, UserID: u.ID, ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.TakeRefreshToken(ctx, "h1")
	if err != nil || got.UserID != u.ID || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("take: %+v, %v", got, err)
	}
	if _, err := store.TakeRefreshToken(ctx, "h1"); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("take repetido: err = %v", err)
	}
	_ = store.DeleteRefreshToken(ctx, "h2")
	if _, err := store.TakeRefreshToken(ctx, "h2"); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("após delete: err = %v", err)
	}
	_ = store.DeleteRefreshTokensOf(ctx, u.ID)
	if _, err := store.TakeRefreshToken(ctx, "h3"); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("após delete do usuário: err = %v", err)
	}
}

func TestInTxRollsBack(t *testing.T) {
	ctx, store := context.Background(), reset(t)
	boom := errors.New("boom")
	err := store.InTx(ctx, func(tx user.Store) error {
		_ = tx.CreateUser(ctx, newUser(t, "ana@example.com", time.Now()))
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.UserByEmail(ctx, "ana@example.com"); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("usuário não deveria existir: err = %v", err)
	}
}

type fakeProducer struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (p *fakeProducer) Produce(_ context.Context, topic, key string, value []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.sent = append(p.sent, topic+"|"+key+"|"+string(value))
	return nil
}

func TestOutboxRelay(t *testing.T) {
	ctx, store := context.Background(), reset(t)
	id := uuid.MustParse("0198c7a4-0000-7000-8000-000000000001")
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := store.WelcomeRequested(ctx, user.WelcomeRequested{UserID: id, Email: "ana@example.com", Name: "Ana", OccurredAt: at}); err != nil {
		t.Fatal(err)
	}

	failing := &fakeProducer{err: errors.New("broker fora")}
	if err := (postgres.Relay{Pool: pool, Producer: failing}).PublishPending(ctx); err == nil {
		t.Fatal("falha do broker deveria voltar como erro")
	}

	producer := &fakeProducer{}
	relay := postgres.Relay{Pool: pool, Producer: producer}
	if err := relay.PublishPending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := relay.PublishPending(ctx); err != nil { // já publicado: não reenvia
		t.Fatal(err)
	}
	want := `user.welcome-requested|` + id.String() +
		`|{"userId":"` + id.String() + `","email":"ana@example.com","name":"Ana","occurredAt":"2026-01-01T12:00:00Z"}`
	if len(producer.sent) != 1 || producer.sent[0] != want {
		t.Fatalf("enviado = %q", producer.sent)
	}
}

type ctxProducer struct{ got context.Context }

func (p *ctxProducer) Produce(ctx context.Context, _, _ string, _ []byte) error {
	p.got = ctx
	return nil
}

func TestOutboxPropagatesTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	store := reset(t)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	e := user.WelcomeRequested{UserID: uuid.New(), Email: "ana@example.com", Name: "Ana", OccurredAt: time.Now()}
	if err := store.WelcomeRequested(ctx, e); err != nil {
		t.Fatal(err)
	}

	producer := &ctxProducer{}
	if err := (postgres.Relay{Pool: pool, Producer: producer}).PublishPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := trace.SpanContextFromContext(producer.got).TraceID(); got != sc.TraceID() {
		t.Fatalf("trace id no produce = %s, quer %s", got, sc.TraceID())
	}
}
