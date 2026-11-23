// Package usertest tem fakes em memória das interfaces de user, para testes sem banco
// (mesma ideia de net/http/httptest).
package usertest

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/dirceurezende/user-template-go/internal/user"
)

// Store guarda tudo em mapas. InTx não faz rollback: os testes não dependem disso.
type Store struct {
	mu     sync.Mutex
	users  map[uuid.UUID]user.User
	tokens map[string]user.RefreshToken

	Published []user.WelcomeRequested // eventos gravados no "outbox"
}

func NewStore() *Store {
	return &Store{users: map[uuid.UUID]user.User{}, tokens: map[string]user.RefreshToken{}}
}

func clone(u user.User) user.User {
	u.Roles = slices.Clone(u.Roles)
	return u
}

func (s *Store) CreateUser(_ context.Context, u user.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.users {
		if other.Email == u.Email {
			return user.ErrEmailTaken
		}
	}
	s.users[u.ID] = clone(u)
	return nil
}

func (s *Store) SaveUser(_ context.Context, u user.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = clone(u)
	return nil
}

func (s *Store) UserByID(_ context.Context, id uuid.UUID) (user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return clone(u), nil
}

func (s *Store) UserByEmail(_ context.Context, email string) (user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.Email == email {
			return clone(u), nil
		}
	}
	return user.User{}, user.ErrNotFound
}

func (s *Store) ListUsers(_ context.Context, offset, limit int) ([]user.User, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]user.User, 0, len(s.users))
	for _, u := range s.users {
		all = append(all, clone(u))
	}
	slices.SortFunc(all, func(a, b user.User) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), strings.Compare(a.ID.String(), b.ID.String()))
	})
	start, end := min(offset, len(all)), min(offset+limit, len(all))
	return all[start:end], len(all), nil
}

func (s *Store) SaveRefreshToken(_ context.Context, t user.RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[t.Hash] = t
	return nil
}

func (s *Store) TakeRefreshToken(_ context.Context, hash string) (user.RefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[hash]
	if !ok {
		return user.RefreshToken{}, user.ErrNotFound
	}
	delete(s.tokens, hash)
	return t, nil
}

func (s *Store) DeleteRefreshToken(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, hash)
	return nil
}

func (s *Store) DeleteRefreshTokensOf(_ context.Context, userID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for h, t := range s.tokens {
		if t.UserID == userID {
			delete(s.tokens, h)
		}
	}
	return nil
}

func (s *Store) InTx(_ context.Context, fn func(user.Store) error) error { return fn(s) }

// TokenCount ajuda a conferir revogação nos testes.
func (s *Store) TokenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens)
}

func (s *Store) WelcomeRequested(_ context.Context, ev user.WelcomeRequested) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Published = append(s.Published, ev)
	return nil
}

// Notifier manda cada boas-vindas no canal Sent (bufferizado) para o teste esperar.
type Notifier struct{ Sent chan string }

func NewNotifier() *Notifier { return &Notifier{Sent: make(chan string, 16)} }

func (n *Notifier) Welcome(_ context.Context, email, _ string) error {
	n.Sent <- email
	return nil
}

// Hasher "hash" reversível e rápido: não use fora de teste.
type Hasher struct{}

func (Hasher) Hash(raw string) (user.PasswordHash, error) {
	return user.PasswordHash("hashed:" + raw), nil
}
func (Hasher) Matches(h user.PasswordHash, raw string) bool { return string(h) == "hashed:"+raw }

// AccessTokens devolve "access-<id>".
type AccessTokens struct{}

func (AccessTokens) Issue(u user.User) (string, time.Duration, error) {
	return "access-" + u.ID.String(), 15 * time.Minute, nil
}

// Clock é um relógio controlável: passe c.Now para Service.Now.
type Clock struct{ T time.Time }

func (c *Clock) Now() time.Time { return c.T }

// NewService monta um Service só com fakes.
func NewService(store *Store, clock *Clock) *user.Service {
	return &user.Service{
		Store: store, Notifier: NewNotifier(), Hasher: Hasher{},
		AccessTokens: AccessTokens{}, RefreshTTL: 7 * 24 * time.Hour, Now: clock.Now,
	}
}
