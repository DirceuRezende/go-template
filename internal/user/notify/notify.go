// Package notify implementa user.Notifier. Log é um stand-in de um envio real
// (e-mail, push...): troque este adapter sem tocar no core.
package notify

import (
	"context"
	"log/slog"
)

type Log struct{}

func (Log) Welcome(ctx context.Context, email, name string) error {
	slog.InfoContext(ctx, "boas-vindas enviadas", "name", name, "email", email)
	return nil
}
