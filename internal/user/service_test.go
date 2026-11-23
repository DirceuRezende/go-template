package user_test

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/dirceurezende/user-template-go/internal/user"
	"github.com/dirceurezende/user-template-go/internal/user/usertest"
)

var ctx = context.Background()

func setup(t *testing.T) (*user.Service, *usertest.Store, *usertest.Clock) {
	t.Helper()
	store := usertest.NewStore()
	clock := &usertest.Clock{T: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	return usertest.NewService(store, clock), store, clock
}

func register(t *testing.T, svc *user.Service, email string) user.User {
	t.Helper()
	u, err := svc.Register(ctx, "Ana", email, "secret123")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestRegister(t *testing.T) {
	svc, _, _ := setup(t)
	u := register(t, svc, "Ana@Example.com")
	if u.PasswordHash != "hashed:secret123" {
		t.Fatalf("senha não passou pelo Hasher: %q", string(u.PasswordHash))
	}
	if _, err := svc.Register(ctx, "Outra", "ANA@example.com", "secret123"); !errors.Is(err, user.ErrEmailTaken) {
		t.Fatalf("email repetido: err = %v", err)
	}
	_, err := svc.Register(ctx, "Bia", "bia@example.com", "1234567")
	if _, ok := errors.AsType[*user.ValidationError](err); !ok {
		t.Fatalf("senha curta: err = %v", err)
	}
}

func TestLogin(t *testing.T) {
	svc, _, _ := setup(t)
	register(t, svc, "ana@example.com")

	tokens, err := svc.Login(ctx, " ANA@example.com", "secret123")
	if err != nil || tokens.AccessToken == "" || len(tokens.RefreshToken) != 43 { // 32 bytes em base64url
		t.Fatalf("login: %+v, %v", tokens, err)
	}
	for _, c := range []struct{ email, password string }{
		{"ana@example.com", "errada123"},
		{"ninguem@example.com", "secret123"},
		{"not-an-email", "secret123"},
	} {
		if _, err := svc.Login(ctx, c.email, c.password); !errors.Is(err, user.ErrInvalidCredentials) {
			t.Errorf("login(%q): err = %v", c.email, err)
		}
	}
}

func TestRefreshRotatesAndExpires(t *testing.T) {
	svc, store, clock := setup(t)
	register(t, svc, "ana@example.com")
	first, _ := svc.Login(ctx, "ana@example.com", "secret123")

	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil || second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh: %+v, %v", second, err)
	}
	if _, err := svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, user.ErrInvalidRefresh) {
		t.Fatalf("token já usado: err = %v", err)
	}

	clock.T = clock.T.Add(7*24*time.Hour + time.Second)
	if _, err := svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, user.ErrInvalidRefresh) {
		t.Fatalf("token expirado: err = %v", err)
	}
	if store.TokenCount() != 0 {
		t.Fatalf("token expirado deveria ser apagado; restam %d", store.TokenCount())
	}
}

func TestLogoutRevokes(t *testing.T) {
	svc, _, _ := setup(t)
	register(t, svc, "ana@example.com")
	tokens, _ := svc.Login(ctx, "ana@example.com", "secret123")

	if err := svc.Logout(ctx, tokens.RefreshToken); err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, tokens.RefreshToken); err != nil {
		t.Fatalf("logout deve ser idempotente: %v", err)
	}
	if _, err := svc.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, user.ErrInvalidRefresh) {
		t.Fatalf("refresh após logout: err = %v", err)
	}
}

func TestDeleteDeactivatesAndRevokesSessions(t *testing.T) {
	svc, store, _ := setup(t)
	u := register(t, svc, "ana@example.com")
	tokens, _ := svc.Login(ctx, "ana@example.com", "secret123")

	if err := svc.Delete(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, u.ID); err != nil {
		t.Fatalf("delete repetido: %v", err)
	}
	got, _ := svc.User(ctx, u.ID)
	if got.Active || store.TokenCount() != 0 {
		t.Fatalf("active=%v tokens=%d", got.Active, store.TokenCount())
	}
	if _, err := svc.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, user.ErrInvalidRefresh) {
		t.Fatalf("refresh de usuário removido: err = %v", err)
	}
	if _, err := svc.Login(ctx, "ana@example.com", "secret123"); !errors.Is(err, user.ErrInvalidCredentials) {
		t.Fatalf("login de usuário removido: err = %v", err)
	}
	if err := svc.Delete(ctx, uuid.New()); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("delete inexistente: err = %v", err)
	}
}

func TestUsersPaginates(t *testing.T) {
	svc, _, clock := setup(t)
	for _, e := range []string{"a@x.com", "b@x.com", "c@x.com"} {
		register(t, svc, e)
		clock.T = clock.T.Add(time.Second)
	}
	page, err := svc.Users(ctx, 1, 2)
	if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].Email != "c@x.com" {
		t.Fatalf("página: %+v, %v", page, err)
	}
}

func TestRenameAndRequestWelcome(t *testing.T) {
	svc, store, clock := setup(t)
	u := register(t, svc, "ana@example.com")

	renamed, err := svc.Rename(ctx, u.ID, "  Bia ")
	if err != nil || renamed.Name != "Bia" {
		t.Fatalf("rename: %+v, %v", renamed, err)
	}
	if _, err := svc.Rename(ctx, uuid.New(), "Bia"); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("rename inexistente: err = %v", err)
	}

	if err := svc.RequestWelcome(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	events := store.Published
	want := user.WelcomeRequested{UserID: u.ID, Email: "ana@example.com", Name: "Bia", OccurredAt: clock.T}
	if len(events) != 1 || events[0] != want {
		t.Fatalf("eventos = %+v", events)
	}

	_ = svc.Delete(ctx, u.ID)
	if err := svc.RequestWelcome(ctx, u.ID); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("welcome de inativo: err = %v", err)
	}
}

func TestSendWelcomeNotifies(t *testing.T) {
	svc, _, _ := setup(t)
	if err := svc.SendWelcome(ctx, user.WelcomeRequested{Email: "ana@example.com", Name: "Ana"}); err != nil {
		t.Fatal(err)
	}
	if got := <-svc.Notifier.(*usertest.Notifier).Sent; got != "ana@example.com" {
		t.Fatalf("notificado = %q", got)
	}
}
