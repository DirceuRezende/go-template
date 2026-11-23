// Package user é o núcleo da feature: domínio, casos de uso (Service) e as
// interfaces (ports) que os adapters dos subpacotes implementam.
// Só stdlib aqui; o golangci-lint (depguard) barra qualquer outro import.
package user

import (
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"uuid"
)

type Role string

const (
	RoleUser  Role = "USER"
	RoleAdmin Role = "ADMIN"
)

const nameMaxLength = 120

var (
	ErrNotFound           = errors.New("usuário não encontrado")
	ErrEmailTaken         = errors.New("email já cadastrado")
	ErrInvalidCredentials = errors.New("credenciais inválidas")
	ErrInvalidRefresh     = errors.New("refresh token inválido ou expirado")
)

// ValidationError é violação de regra de entrada/domínio; a borda HTTP responde 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// PasswordHash é a senha já com hash. String/LogValue mascaram para não vazar em log.
type PasswordHash string

func (PasswordHash) String() string       { return "***" }
func (PasswordHash) LogValue() slog.Value { return slog.StringValue("***") }

// User é o agregado. Campos exportados para leitura; mudanças de estado passam pelos métodos.
type User struct {
	ID           uuid.UUID
	Email        string
	Name         string
	PasswordHash PasswordHash
	Roles        []Role
	Active       bool
	CreatedAt    time.Time
}

// New cria um usuário novo com as regras de cadastro: papel USER, ativo, ID gerado aqui.
func New(email, name string, hash PasswordHash, now time.Time) (User, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	name, err = validateName(name)
	if err != nil {
		return User{}, err
	}
	if hash == "" {
		return User{}, invalid("Hash de senha é obrigatório")
	}
	return User{ID: uuid.New(), Email: email, Name: name, PasswordHash: hash,
		Roles: []Role{RoleUser}, Active: true, CreatedAt: now}, nil
}

func (u *User) Rename(name string) error {
	name, err := validateName(name)
	if err != nil {
		return err
	}
	u.Name = name
	return nil
}

func (u *User) Grant(r Role) {
	if !u.HasRole(r) {
		u.Roles = append(u.Roles, r)
	}
}

func (u *User) HasRole(r Role) bool { return slices.Contains(u.Roles, r) }

func (u *User) Deactivate() { u.Active = false }

var emailFormat = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// NormalizeEmail aplica trim + minúsculas e valida o formato: emails comparam sem diferenciar caixa.
func NormalizeEmail(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", invalid("Email é obrigatório")
	}
	if !emailFormat.MatchString(s) {
		return "", invalid("Email inválido: " + s)
	}
	return s, nil
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("Nome é obrigatório")
	}
	if len([]rune(name)) > nameMaxLength {
		return "", invalid("Nome excede 120 caracteres")
	}
	return name, nil
}

// WelcomeRequested é o evento de domínio publicado pelo pedido de boas-vindas.
type WelcomeRequested struct {
	UserID     uuid.UUID
	Email      string
	Name       string
	OccurredAt time.Time
}
