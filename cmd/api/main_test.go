package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/dirceurezende/user-template-go/internal/user/usertest"
)

// TestWelcomeFlow: HTTP → outbox → relay → Kafka → consumer → Notifier, com tudo de verdade.
func TestWelcomeFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e com containers")
	}
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	kc, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.5.0") // o módulo do testcontainers-go só suporta esta imagem
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kc.Terminate(context.Background()) })
	brokers, err := kc.Brokers(ctx)
	if err != nil {
		t.Fatal(err)
	}

	notifier := usertest.NewNotifier()
	a, err := newApp(ctx, config{
		DatabaseURL:    pg.MustConnectionString(ctx, "sslmode=disable"),
		KafkaBrokers:   brokers,
		JWTSecret:      []byte(devJWTSecret),
		AccessTTL:      time.Minute,
		RefreshTTL:     time.Hour,
		OutboxInterval: 100 * time.Millisecond,
	}, notifier)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	a.start(ctx, &wg)
	t.Cleanup(func() { cancel(); wg.Wait(); a.close() })
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)

	post := func(path, auth, body string) (int, []byte) {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	_, body := post("/api/users", "", `{"name":"Ana","email":"ana@example.com","password":"secret123"}`)
	var created struct{ ID string }
	_ = json.Unmarshal(body, &created)
	_, body = post("/api/auth/login", "", `{"email":"ana@example.com","password":"secret123"}`)
	var tokens struct{ AccessToken string }
	_ = json.Unmarshal(body, &tokens)

	if status, body := post("/api/users/"+created.ID+"/welcome", "Bearer "+tokens.AccessToken, ""); status != http.StatusAccepted {
		t.Fatalf("welcome: status %d: %s", status, body)
	}
	select {
	case email := <-notifier.Sent:
		if email != "ana@example.com" {
			t.Fatalf("notificado = %q", email)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("boas-vindas não chegaram pelo Kafka em 30s")
	}
}
