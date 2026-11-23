package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/dirceurezende/user-template-go/internal/user"
	"github.com/dirceurezende/user-template-go/internal/user/httpapi"
	"github.com/dirceurezende/user-template-go/internal/user/security"
	"github.com/dirceurezende/user-template-go/internal/user/usertest"
)

func init() { gin.SetMode(gin.TestMode) }

// api sobe o handler com Store em memória e JWT de verdade.
type api struct {
	t       *testing.T
	handler http.Handler
	store   *usertest.Store
	jwt     security.JWT
}

type session struct{ id, access, refresh string }

func (s session) bearer() string { return "Bearer " + s.access }

func newAPI(t *testing.T) *api {
	store := usertest.NewStore()
	jwt := security.JWT{Secret: []byte("test-secret-0123456789abcdef0123456789"), TTL: 15 * time.Minute}
	svc := &user.Service{Store: store, Hasher: usertest.Hasher{},
		AccessTokens: jwt, RefreshTTL: time.Hour}
	return &api{t, httpapi.New(svc, jwt), store, jwt}
}

func (a *api) do(method, path, auth, body string) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	return rec
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, quer %d; corpo: %s", rec.Code, status, rec.Body)
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("json: %v; corpo: %s", err, rec.Body)
	}
	return v
}

func (a *api) signup(email string) session {
	a.t.Helper()
	rec := a.do("POST", "/api/users", "", `{"name":"Ana","email":"`+email+`","password":"secret123"}`)
	expect(a.t, rec, 201)
	s := a.login(email, "secret123")
	s.id = decode[map[string]any](a.t, rec)["id"].(string)
	return s
}

func (a *api) login(email, password string) session {
	a.t.Helper()
	rec := a.do("POST", "/api/auth/login", "", `{"email":"`+email+`","password":"`+password+`"}`)
	expect(a.t, rec, 200)
	body := decode[map[string]any](a.t, rec)
	return session{access: body["accessToken"].(string), refresh: body["refreshToken"].(string)}
}

func (a *api) admin() session {
	a.t.Helper()
	u, _ := user.New("admin@example.com", "Admin", "hashed:admin12345", time.Now())
	u.Grant(user.RoleAdmin)
	if err := a.store.CreateUser(context.Background(), u); err != nil {
		a.t.Fatal(err)
	}
	s := a.login("admin@example.com", "admin12345")
	s.id = u.ID.String()
	return s
}

func TestRegister(t *testing.T) {
	a := newAPI(t)
	rec := a.do("POST", "/api/users", "", `{"name":"Ana","email":"Dup@Example.com","password":"secret123"}`)
	expect(t, rec, 201)
	body := decode[map[string]any](t, rec)
	if rec.Header().Get("Location") != "/api/users/"+body["id"].(string) {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	if _, ok := body["password"]; ok || body["email"] != "dup@example.com" || body["roles"].([]any)[0] != "USER" {
		t.Fatalf("corpo = %v", body)
	}

	rec = a.do("POST", "/api/users", "", `{"name":"Outra","email":"DUP@example.com","password":"secret123"}`)
	expect(t, rec, 409)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}

	for _, body := range []string{
		`{"name":"Ana","email":"a@example.com","password":"123"}`,
		`{"name":"Ana","email":"not-an-email","password":"secret123"}`,
		`{"name":" ","email":"a@example.com","password":"secret123"}`,
		`{`,
	} {
		expect(t, a.do("POST", "/api/users", "", body), 400)
	}
}

func TestProtectedRoutesNeedAValidToken(t *testing.T) {
	a := newAPI(t)
	id := uuid.New().String()
	expect(t, a.do("GET", "/api/users", "", ""), 401)
	expect(t, a.do("GET", "/api/users/"+id, "", ""), 401)
	expect(t, a.do("DELETE", "/api/users/"+id, "", ""), 401)
	expect(t, a.do("POST", "/api/users/"+id+"/welcome", "", ""), 401)

	ana := a.signup("ana@example.com")
	expect(t, a.do("GET", "/api/users/"+ana.id, ana.bearer()+"x", ""), 401)

	expired := security.JWT{Secret: a.jwt.Secret, TTL: time.Minute, Now: func() time.Time { return time.Now().Add(-time.Hour) }}
	u, _ := a.store.UserByID(context.Background(), uuid.MustParse(ana.id))
	token, _, _ := expired.Issue(u)
	expect(t, a.do("GET", "/api/users/"+ana.id, "Bearer "+token, ""), 401)
}

func TestLogin(t *testing.T) {
	a := newAPI(t)
	a.signup("ana@example.com")
	rec := a.do("POST", "/api/auth/login", "", `{"email":"ana@example.com","password":"secret123"}`)
	expect(t, rec, 200)
	body := decode[map[string]any](t, rec)
	if body["tokenType"] != "Bearer" || body["expiresIn"] != float64(900) {
		t.Fatalf("corpo = %v", body)
	}
	expect(t, a.do("POST", "/api/auth/login", "", `{"email":"ana@example.com","password":"errada123"}`), 401)
	expect(t, a.do("POST", "/api/auth/login", "", `{"email":"x@example.com","password":"secret123"}`), 401)
	expect(t, a.do("POST", "/api/auth/login", "", `{"email":"","password":""}`), 400)
}

func TestUserManagesOnlyThemselves(t *testing.T) {
	a := newAPI(t)
	ana, bob := a.signup("ana@example.com"), a.signup("bob@example.com")

	expect(t, a.do("GET", "/api/users/"+ana.id, ana.bearer(), ""), 200)
	rec := a.do("PUT", "/api/users/"+ana.id, ana.bearer(), `{"name":"  Ana 2 "}`)
	expect(t, rec, 200)
	if decode[map[string]any](t, rec)["name"] != "Ana 2" {
		t.Fatalf("rename sem trim: %s", rec.Body)
	}
	expect(t, a.do("PUT", "/api/users/"+ana.id, ana.bearer(), `{"name":" "}`), 400)

	expect(t, a.do("GET", "/api/users/"+bob.id, ana.bearer(), ""), 403)
	expect(t, a.do("PUT", "/api/users/"+bob.id, ana.bearer(), `{"name":"x"}`), 403)
	expect(t, a.do("DELETE", "/api/users/"+bob.id, ana.bearer(), ""), 403)
	expect(t, a.do("POST", "/api/users/"+bob.id+"/welcome", ana.bearer(), ""), 403)
	expect(t, a.do("GET", "/api/users", ana.bearer(), ""), 403)

	expect(t, a.do("POST", "/api/users/"+ana.id+"/welcome", ana.bearer(), ""), 202)
	expect(t, a.do("DELETE", "/api/users/"+ana.id, ana.bearer(), ""), 204)
}

func TestAdminManagesAnyone(t *testing.T) {
	a := newAPI(t)
	admin, ana := a.admin(), a.signup("ana@example.com")
	missing := uuid.New().String()

	expect(t, a.do("GET", "/api/users/"+ana.id, admin.bearer(), ""), 200)
	expect(t, a.do("GET", "/api/users/"+missing, admin.bearer(), ""), 404)
	expect(t, a.do("GET", "/api/users/not-a-uuid", admin.bearer(), ""), 400)
	expect(t, a.do("PUT", "/api/users/"+ana.id, admin.bearer(), `{"name":"Renomeada"}`), 200)
	expect(t, a.do("PUT", "/api/users/"+missing, admin.bearer(), `{"name":"x"}`), 404)
	expect(t, a.do("POST", "/api/users/"+ana.id+"/welcome", admin.bearer(), ""), 202)
	expect(t, a.do("POST", "/api/users/"+missing+"/welcome", admin.bearer(), ""), 404)
	expect(t, a.do("DELETE", "/api/users/"+ana.id, admin.bearer(), ""), 204)
	expect(t, a.do("DELETE", "/api/users/"+ana.id, admin.bearer(), ""), 204) // soft delete idempotente
	expect(t, a.do("DELETE", "/api/users/"+missing, admin.bearer(), ""), 404)
	expect(t, a.do("POST", "/api/users/"+ana.id+"/welcome", admin.bearer(), ""), 404) // inativo
}

func TestListIsPaginatedAndValidated(t *testing.T) {
	a := newAPI(t)
	admin := a.admin()
	a.signup("ana@example.com")
	a.signup("bob@example.com")

	rec := a.do("GET", "/api/users?page=1&size=2", admin.bearer(), "")
	expect(t, rec, 200)
	body := decode[map[string]any](t, rec)
	if body["total"] != float64(3) || body["page"] != float64(1) || body["size"] != float64(2) || len(body["items"].([]any)) != 1 {
		t.Fatalf("página = %v", body)
	}
	body = decode[map[string]any](t, a.do("GET", "/api/users", admin.bearer(), ""))
	if body["page"] != float64(0) || body["size"] != float64(20) {
		t.Fatalf("defaults = %v", body)
	}
	for _, q := range []string{"page=-1", "size=0", "size=101", "page=abc"} {
		expect(t, a.do("GET", "/api/users?"+q, admin.bearer(), ""), 400)
	}
}

func TestRefreshRotatesAndLogoutRevokes(t *testing.T) {
	a := newAPI(t)
	ana := a.signup("ana@example.com")
	refresh := func(token string) *httptest.ResponseRecorder {
		return a.do("POST", "/api/auth/refresh", "", `{"refreshToken":"`+token+`"}`)
	}

	expect(t, refresh(ana.refresh), 200)
	expect(t, refresh(ana.refresh), 401)

	again := a.login("ana@example.com", "secret123")
	expect(t, a.do("POST", "/api/auth/logout", "", `{"refreshToken":"`+again.refresh+`"}`), 204)
	expect(t, refresh(again.refresh), 401)
	expect(t, a.do("POST", "/api/auth/logout", "", `{}`), 400)
}

func TestDeletedUserCannotLoginOrRefresh(t *testing.T) {
	a := newAPI(t)
	ana := a.signup("ana@example.com")
	expect(t, a.do("DELETE", "/api/users/"+ana.id, ana.bearer(), ""), 204)
	expect(t, a.do("POST", "/api/auth/refresh", "", `{"refreshToken":"`+ana.refresh+`"}`), 401)
	expect(t, a.do("POST", "/api/auth/login", "", `{"email":"ana@example.com","password":"secret123"}`), 401)
}

// TestOpenAPICoversRoutes falha se uma rota /api existir sem a operação correspondente no openapi.yaml.
func TestOpenAPICoversRoutes(t *testing.T) {
	a := newAPI(t)
	rec := a.do("GET", "/openapi.yaml", "", "")
	expect(t, rec, 200)
	spec := rec.Body.String()
	expect(t, a.do("GET", "/docs", "", ""), 200)

	for _, r := range a.handler.(*gin.Engine).Routes() {
		if !strings.HasPrefix(r.Path, "/api/") {
			continue
		}
		path := strings.ReplaceAll(r.Path, ":id", "{id}")
		_, block, ok := strings.Cut(spec, "\n  "+path+":\n")
		block, _, _ = strings.Cut(block, "\n  /") // até o próximo path
		if !ok || !strings.Contains("\n"+block, "\n    "+strings.ToLower(r.Method)+":\n") {
			t.Errorf("openapi.yaml sem %s %s", r.Method, path)
		}
	}
}
