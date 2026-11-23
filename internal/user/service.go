package user

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
	"uuid"
)

// Interfaces (ports) declaradas aqui, no consumidor. Implementações: subpacotes
// postgres, security, notify; fakes em usertest.

// Store é a persistência. Uma interface só porque é um banco só; InTx dá atomicidade
// para casos de uso que escrevem mais de uma coisa.
type Store interface {
	CreateUser(ctx context.Context, u User) error // ErrEmailTaken se o email já existe
	SaveUser(ctx context.Context, u User) error
	UserByID(ctx context.Context, id uuid.UUID) (User, error)    // ErrNotFound
	UserByEmail(ctx context.Context, email string) (User, error) // ErrNotFound
	ListUsers(ctx context.Context, offset, limit int) (users []User, total int, err error)

	SaveRefreshToken(ctx context.Context, t RefreshToken) error
	TakeRefreshToken(ctx context.Context, hash string) (RefreshToken, error) // apaga e devolve; ErrNotFound
	DeleteRefreshToken(ctx context.Context, hash string) error
	DeleteRefreshTokensOf(ctx context.Context, userID uuid.UUID) error

	// WelcomeRequested registra o evento para publicação (outbox). Fica no Store para
	// entrar na mesma transação das outras escritas quando chamado via InTx.
	WelcomeRequested(ctx context.Context, e WelcomeRequested) error

	InTx(ctx context.Context, fn func(Store) error) error
}

// RefreshToken persistido: só o hash, nunca o valor que o cliente recebe.
type RefreshToken struct {
	Hash      string
	UserID    uuid.UUID
	ExpiresAt time.Time
}

type Notifier interface {
	Welcome(ctx context.Context, email, name string) error
}

type Hasher interface {
	Hash(raw string) (PasswordHash, error)
	Matches(hash PasswordHash, raw string) bool
}

type AccessTokens interface {
	Issue(u User) (token string, ttl time.Duration, err error)
}

// Service reúne os casos de uso. Dependências por campo exportado, montado em cmd/api.
type Service struct {
	Store        Store
	Notifier     Notifier
	Hasher       Hasher
	AccessTokens AccessTokens
	RefreshTTL   time.Duration
	Now          func() time.Time // nil = time.Now; testes fixam o relógio
}

const MinPasswordLength = 8

type Page struct {
	Items             []User
	Page, Size, Total int
}

// Tokens é o par entregue no login/refresh. RefreshToken é o valor cru (só o cliente tem).
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Service) Register(ctx context.Context, name, email, password string) (User, error) {
	// Política de senha precisa da senha crua, então fica antes do hash (não no domínio).
	if len(password) < MinPasswordLength {
		return User{}, invalid("Senha deve ter ao menos 8 caracteres")
	}
	hash, err := s.Hasher.Hash(password)
	if err != nil {
		return User{}, err
	}
	u, err := New(email, name, hash, s.now())
	if err != nil {
		return User{}, err
	}
	return u, s.Store.CreateUser(ctx, u)
}

func (s *Service) User(ctx context.Context, id uuid.UUID) (User, error) {
	return s.Store.UserByID(ctx, id)
}

func (s *Service) Users(ctx context.Context, page, size int) (Page, error) {
	users, total, err := s.Store.ListUsers(ctx, page*size, size)
	return Page{Items: users, Page: page, Size: size, Total: total}, err
}

func (s *Service) Rename(ctx context.Context, id uuid.UUID, name string) (User, error) {
	u, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if err := u.Rename(name); err != nil {
		return User{}, err
	}
	return u, s.Store.SaveUser(ctx, u)
}

// Delete é soft delete e derruba as sessões (refresh tokens). Repetir é ok: continua 204.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	return s.Store.InTx(ctx, func(tx Store) error {
		u, err := tx.UserByID(ctx, id)
		if err != nil {
			return err
		}
		u.Deactivate()
		if err := tx.SaveUser(ctx, u); err != nil {
			return err
		}
		return tx.DeleteRefreshTokensOf(ctx, id)
	})
}

func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return Tokens{}, ErrInvalidCredentials
	}
	u, err := s.Store.UserByEmail(ctx, email)
	if errors.Is(err, ErrNotFound) {
		// shortcut: usuário inexistente responde mais rápido que senha errada (timing), upgrade: hash dummy + rate limit
		return Tokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return Tokens{}, err
	}
	if !u.Active || !s.Hasher.Matches(u.PasswordHash, password) {
		return Tokens{}, ErrInvalidCredentials
	}
	return s.issue(ctx, s.Store, u)
}

// Refresh troca o refresh token por um par novo. Cada token vale uma vez (rotação):
// é apagado mesmo se já expirou.
// shortcut: reuso de token já rotacionado não é detectado, upgrade: famílias de token + revogar a família no reuso
func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	var tokens Tokens
	err := s.Store.InTx(ctx, func(tx Store) error {
		stored, err := tx.TakeRefreshToken(ctx, hashToken(raw))
		if errors.Is(err, ErrNotFound) {
			return ErrInvalidRefresh
		}
		if err != nil {
			return err
		}
		if !stored.ExpiresAt.After(s.now()) {
			return ErrInvalidRefresh
		}
		u, err := tx.UserByID(ctx, stored.UserID)
		if errors.Is(err, ErrNotFound) || (err == nil && !u.Active) {
			return ErrInvalidRefresh
		}
		if err != nil {
			return err
		}
		tokens, err = s.issue(ctx, tx, u)
		return err
	})
	return tokens, err
}

// Logout revoga só o refresh token informado; o access token vale até expirar (stateless).
func (s *Service) Logout(ctx context.Context, raw string) error {
	return s.Store.DeleteRefreshToken(ctx, hashToken(raw))
}

// RequestWelcome só registra o evento (outbox); o envio acontece no consumer Kafka.
func (s *Service) RequestWelcome(ctx context.Context, id uuid.UUID) error {
	u, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if !u.Active {
		return ErrNotFound
	}
	return s.Store.WelcomeRequested(ctx, WelcomeRequested{UserID: u.ID, Email: u.Email, Name: u.Name, OccurredAt: s.now()})
}

// SendWelcome roda no consumer. Entrega é at-least-once: precisa tolerar repetição.
func (s *Service) SendWelcome(ctx context.Context, e WelcomeRequested) error {
	return s.Notifier.Welcome(ctx, e.Email, e.Name)
}

func (s *Service) issue(ctx context.Context, store Store, u User) (Tokens, error) {
	access, ttl, err := s.AccessTokens.Issue(u)
	if err != nil {
		return Tokens{}, err
	}
	raw := make([]byte, 32)
	rand.Read(raw) // nunca falha (crypto/rand, Go 1.24+)
	refresh := base64.RawURLEncoding.EncodeToString(raw)
	if err := store.SaveRefreshToken(ctx, RefreshToken{Hash: hashToken(refresh), UserID: u.ID, ExpiresAt: s.now().Add(s.RefreshTTL)}); err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: ttl}, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
