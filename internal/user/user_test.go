package user

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestNewAppliesRegistrationRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	u, err := New("  Ana@Example.COM ", "  Ana ", "h", now)
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ana@example.com" || u.Name != "Ana" || !u.Active || !u.CreatedAt.Equal(now) {
		t.Fatalf("usuário inesperado: %+v", u)
	}
	if len(u.Roles) != 1 || u.Roles[0] != RoleUser {
		t.Fatalf("papéis = %v, quer [USER]", u.Roles)
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	cases := map[string]struct{ email, name string }{
		"email vazio":    {"", "Ana"},
		"email inválido": {"not-an-email", "Ana"},
		"nome em branco": {"a@example.com", "  "},
		"nome longo":     {"a@example.com", strings.Repeat("a", 121)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(c.email, c.name, "h", time.Now())
			if _, ok := errors.AsType[*ValidationError](err); !ok {
				t.Fatalf("err = %v, quer *ValidationError", err)
			}
		})
	}
}

func TestRenameAndGrant(t *testing.T) {
	u, _ := New("a@example.com", "Ana", "h", time.Now())
	if err := u.Rename("  Bia "); err != nil || u.Name != "Bia" {
		t.Fatalf("Rename: name=%q err=%v", u.Name, err)
	}
	if err := u.Rename(" "); err == nil {
		t.Fatal("Rename em branco deveria falhar")
	}
	u.Grant(RoleAdmin)
	u.Grant(RoleAdmin)
	if len(u.Roles) != 2 || !u.HasRole(RoleAdmin) {
		t.Fatalf("papéis = %v", u.Roles)
	}
}

func TestPasswordHashIsMasked(t *testing.T) {
	h := PasswordHash("$2a$secret")
	if s := fmt.Sprint(h); s != "***" {
		t.Fatalf("fmt = %q", s)
	}
	var b strings.Builder
	slog.New(slog.NewTextHandler(&b, nil)).Info("x", "hash", h)
	if strings.Contains(b.String(), "secret") {
		t.Fatalf("log vazou o hash: %s", b.String())
	}
}
