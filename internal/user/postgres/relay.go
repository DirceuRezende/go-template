package postgres

import (
	"context"
	"log/slog"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Producer publica uma mensagem e só retorna depois do ack do broker. Implementado em kafka.
type Producer interface {
	Produce(ctx context.Context, topic, key string, value []byte) error
}

// Relay lê o outbox e publica. Só marca como publicado depois do ack; se falhar no meio
// do lote, a transação volta e tudo é reenviado no próximo ciclo (at-least-once).
// shortcut: linhas publicadas nunca são apagadas, upgrade: job de limpeza por published_at
type Relay struct {
	Pool     *pgxpool.Pool
	Producer Producer
	Interval time.Duration
}

const relayBatchSize = 100

// Run roda até o ctx ser cancelado.
func (r Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.PublishPending(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "outbox: falha ao publicar, nova tentativa no próximo ciclo", "err", err)
			}
		}
	}
}

type outboxRow struct {
	ID      uuid.UUID
	Topic   string
	Key     string
	Payload string
}

// PublishPending publica um lote. SKIP LOCKED deixa várias instâncias rodarem o relay sem duplicar o lote.
func (r Relay) PublishPending(ctx context.Context) error {
	return pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, _ := tx.Query(ctx, `SELECT id, topic, message_key, payload FROM outbox
			WHERE published_at IS NULL ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED`, relayBatchSize)
		pending, err := pgx.CollectRows(rows, pgx.RowToStructByPos[outboxRow])
		if err != nil || len(pending) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(pending))
		for i, m := range pending {
			sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := r.Producer.Produce(sendCtx, m.Topic, m.Key, []byte(m.Payload))
			cancel()
			if err != nil {
				return err
			}
			ids[i] = m.ID
		}
		_, err = tx.Exec(ctx, `UPDATE outbox SET published_at = $1 WHERE id = ANY($2)`, time.Now(), ids)
		return err
	})
}
