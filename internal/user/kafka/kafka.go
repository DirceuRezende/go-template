// Package kafka é o adapter de mensageria (franz-go): Producer usado pelo relay do
// outbox e Consumer que entrega WelcomeRequested ao user.Service.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
	"uuid"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel/codes"

	"github.com/dirceurezende/user-template-go/internal/user"
)

// WelcomeTopic e welcomeMessage: contrato do tópico do lado do consumidor
// (o produtor, em postgres, tem a própria cópia de propósito).
const WelcomeTopic = "user.welcome-requested"

type welcomeMessage struct {
	UserID     string    `json:"userId"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	OccurredAt time.Time `json:"occurredAt"`
}

// EnsureTopics cria os tópicos do app se não existirem (3 partições, 1 réplica: só dev).
func EnsureTopics(ctx context.Context, client *kgo.Client) error {
	resp, err := kadm.NewClient(client).CreateTopics(ctx, 3, 1, nil, WelcomeTopic)
	if err != nil {
		return err
	}
	for _, t := range resp {
		if t.Err != nil && !errors.Is(t.Err, kerr.TopicAlreadyExists) {
			return t.Err
		}
	}
	return nil
}

// Producer implementa postgres.Producer.
type Producer struct{ Client *kgo.Client }

func (p Producer) Produce(ctx context.Context, topic, key string, value []byte) error {
	return p.Client.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte(key), Value: value}).FirstErr()
}

// ConsumerOpts são as opções do client do consumer: grupo, tópico, lê desde o início
// num grupo novo e commita só o que já foi processado (at-least-once).
func ConsumerOpts(group string) []kgo.Opt {
	return []kgo.Opt{
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(WelcomeTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.AutoCommitMarks(),
	}
}

// Consumer entrega WelcomeRequested ao Service. O client precisa ter sido criado com ConsumerOpts.
type Consumer struct {
	Client  *kgo.Client
	Service *user.Service
	Tracer  *kotel.Tracer // o mesmo dos hooks do client; continua o trace vindo no header do record
}

// Run consome até o ctx ser cancelado.
// shortcut: mensagem que falha é logada e pulada (sem retry nem DLQ), upgrade: retry com backoff + tópico .DLT
func (c Consumer) Run(ctx context.Context) {
	for {
		fetches := c.Client.PollFetches(ctx)
		if ctx.Err() != nil {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			slog.WarnContext(ctx, "kafka: erro no fetch", "topic", topic, "partition", partition, "err", err)
		})
		fetches.EachRecord(func(r *kgo.Record) {
			rctx, span := c.Tracer.WithProcessSpan(r)
			if err := c.handle(rctx, r.Value); err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				slog.ErrorContext(rctx, "kafka: mensagem descartada", "topic", r.Topic, "offset", r.Offset, "err", err)
			}
			span.End()
			c.Client.MarkCommitRecords(r)
		})
	}
}

func (c Consumer) handle(ctx context.Context, payload []byte) error {
	var m welcomeMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	id, err := uuid.Parse(m.UserID)
	if err != nil {
		return err
	}
	return c.Service.SendWelcome(ctx, user.WelcomeRequested{UserID: id, Email: m.Email, Name: m.Name, OccurredAt: m.OccurredAt})
}
