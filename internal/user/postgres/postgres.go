// Package postgres implementa user.Store (inclusive o outbox) com pgx e SQL escrito à mão.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/dirceurezende/user-template-go/internal/user"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate aplica as migrations pendentes. Migration aplicada nunca é editada: crie outra.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	dir, _ := fs.Sub(migrations, "migrations")
	p, err := goose.NewProvider(goose.DialectPostgres, db, dir)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// querier é o que pool e tx têm em comum: o mesmo Store roda dentro ou fora de transação.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	pool *pgxpool.Pool
	q    querier
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, q: pool} }

func (s *Store) InTx(ctx context.Context, fn func(user.Store) error) error {
	if _, inTx := s.q.(pgx.Tx); inTx {
		return fn(s)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Store{pool: s.pool, q: tx})
	})
}

// Papéis vêm agregados numa coluna; DESC deixa USER antes de ADMIN, como no template Java.
const selectUser = `
SELECT u.id, u.email, u.name, u.password_hash, u.active, u.created_at, array_agg(r.role ORDER BY r.role DESC)
FROM users u JOIN user_roles r ON r.user_id = u.id`

func scanUser(row pgx.Row) (user.User, error) {
	var u user.User
	var roles []string
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.Active, &u.CreatedAt, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, user.ErrNotFound
	}
	for _, r := range roles {
		u.Roles = append(u.Roles, user.Role(r))
	}
	return u, err
}

// atomic é InTx já com o *Store da transação.
func (s *Store) atomic(ctx context.Context, fn func(tx *Store) error) error {
	return s.InTx(ctx, func(tx user.Store) error { return fn(tx.(*Store)) })
}

func (s *Store) CreateUser(ctx context.Context, u user.User) error {
	return s.atomic(ctx, func(tx *Store) error {
		_, err := tx.q.Exec(ctx, `INSERT INTO users (id, email, name, password_hash, active, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, u.ID, u.Email, u.Name, string(u.PasswordHash), u.Active, u.CreatedAt)
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.ConstraintName == "uk_users_email" {
			return user.ErrEmailTaken
		}
		if err != nil {
			return err
		}
		return tx.insertRoles(ctx, u)
	})
}

func (s *Store) SaveUser(ctx context.Context, u user.User) error {
	return s.atomic(ctx, func(tx *Store) error {
		tag, err := tx.q.Exec(ctx, `UPDATE users SET name = $2, password_hash = $3, active = $4 WHERE id = $1`,
			u.ID, u.Name, string(u.PasswordHash), u.Active)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return user.ErrNotFound
		}
		if _, err := tx.q.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		return tx.insertRoles(ctx, u)
	})
}

func (s *Store) insertRoles(ctx context.Context, u user.User) error {
	roles := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = string(r)
	}
	_, err := s.q.Exec(ctx, `INSERT INTO user_roles (user_id, role) SELECT $1, unnest($2::text[])`, u.ID, roles)
	return err
}

func (s *Store) UserByID(ctx context.Context, id uuid.UUID) (user.User, error) {
	return scanUser(s.q.QueryRow(ctx, selectUser+` WHERE u.id = $1 GROUP BY u.id`, id))
}

func (s *Store) UserByEmail(ctx context.Context, email string) (user.User, error) {
	return scanUser(s.q.QueryRow(ctx, selectUser+` WHERE u.email = $1 GROUP BY u.id`, email))
}

func (s *Store) ListUsers(ctx context.Context, offset, limit int) ([]user.User, int, error) {
	var total int
	if err := s.q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.q.Query(ctx, selectUser+` GROUP BY u.id ORDER BY u.created_at, u.id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	users, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (user.User, error) { return scanUser(r) })
	return users, total, err
}

func (s *Store) SaveRefreshToken(ctx context.Context, t user.RefreshToken) error {
	_, err := s.q.Exec(ctx, `INSERT INTO refresh_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		t.Hash, t.UserID, t.ExpiresAt)
	return err
}

func (s *Store) TakeRefreshToken(ctx context.Context, hash string) (user.RefreshToken, error) {
	t := user.RefreshToken{Hash: hash}
	err := s.q.QueryRow(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1 RETURNING user_id, expires_at`, hash).
		Scan(&t.UserID, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.RefreshToken{}, user.ErrNotFound
	}
	return t, err
}

func (s *Store) DeleteRefreshToken(ctx context.Context, hash string) error {
	_, err := s.q.Exec(ctx, `DELETE FROM refresh_tokens WHERE token_hash = $1`, hash)
	return err
}

func (s *Store) DeleteRefreshTokensOf(ctx context.Context, userID uuid.UUID) error {
	_, err := s.q.Exec(ctx, `DELETE FROM refresh_tokens WHERE user_id = $1`, userID)
	return err
}

// WelcomeTopic e welcomeMessage são o contrato do tópico do lado do produtor.
// O consumer (pacote kafka) tem a própria cópia de propósito: cada lado evolui o seu.
const WelcomeTopic = "user.welcome-requested"

type welcomeMessage struct {
	UserID     string    `json:"userId"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	OccurredAt time.Time `json:"occurredAt"`
}

// WelcomeRequested grava no outbox (mesma transação do chamador, se houver); quem publica é o Relay.
func (s *Store) WelcomeRequested(ctx context.Context, e user.WelcomeRequested) error {
	payload, err := json.Marshal(welcomeMessage{e.UserID.String(), e.Email, e.Name, e.OccurredAt})
	if err != nil {
		return err
	}
	// chave = userId: mensagens do mesmo usuário caem na mesma partição, em ordem
	trace := propagation.MapCarrier{} // contexto do trace da requisição; o Relay o devolve ao Kafka
	otel.GetTextMapPropagator().Inject(ctx, trace)
	_, err = s.q.Exec(ctx, `INSERT INTO outbox (id, topic, message_key, payload, created_at, trace_context) VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.New(), WelcomeTopic, e.UserID.String(), string(payload), e.OccurredAt, map[string]string(trace))
	return err
}
