// Package httpapi é o adapter HTTP (Gin): traduz requisições para o user.Service
// e erros do domínio para status + problem+json (RFC 9457).
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/gin-gonic/gin"

	"github.com/dirceurezende/user-template-go/internal/user"
	"github.com/dirceurezende/user-template-go/internal/user/security"
)

// New monta as rotas. Públicas: cadastro e /api/auth/*; o resto exige Bearer.
func New(svc *user.Service, tokens security.JWT) http.Handler {
	r := gin.New()
	r.ContextWithFallback = true // *gin.Context passa a ser o ctx da requisição (cancelamento chega no banco)
	r.Use(gin.Recovery())
	h := handler{svc}

	api := r.Group("/api")
	api.POST("/users", h.register)
	api.POST("/auth/login", h.login)
	api.POST("/auth/refresh", h.refresh)
	api.POST("/auth/logout", h.logout)

	private := api.Group("", authenticate(tokens))
	private.GET("/users", requireRole(user.RoleAdmin), h.list)
	private.GET("/users/:id", selfOrAdmin, h.get)
	private.PUT("/users/:id", selfOrAdmin, h.update)
	private.DELETE("/users/:id", selfOrAdmin, h.delete)
	private.POST("/users/:id/welcome", selfOrAdmin, h.welcome)
	return r
}

type handler struct{ svc *user.Service }

type registerRequest struct {
	Name     string `json:"name" binding:"required,max=120"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type updateRequest struct {
	Name string `json:"name" binding:"required,max=120"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

type userResponse struct {
	ID        uuid.UUID   `json:"id"`
	Name      string      `json:"name"`
	Email     string      `json:"email"`
	Roles     []user.Role `json:"roles"`
	Active    bool        `json:"active"`
	CreatedAt time.Time   `json:"createdAt"`
}

type pageResponse struct {
	Items []userResponse `json:"items"`
	Page  int            `json:"page"`
	Size  int            `json:"size"`
	Total int            `json:"total"`
}

type tokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
}

func toResponse(u user.User) userResponse {
	return userResponse{u.ID, u.Name, u.Email, u.Roles, u.Active, u.CreatedAt.UTC()}
}

func toTokenResponse(t user.Tokens) tokenResponse {
	return tokenResponse{t.AccessToken, t.RefreshToken, "Bearer", int64(t.ExpiresIn.Seconds())}
}

func (h handler) register(c *gin.Context) {
	var req registerRequest
	if !bind(c, c.ShouldBindJSON(&req)) {
		return
	}
	u, err := h.svc.Register(c, req.Name, req.Email, req.Password)
	if errors.Is(err, user.ErrEmailTaken) {
		problem(c, http.StatusConflict, "Email já cadastrado: "+strings.ToLower(strings.TrimSpace(req.Email)))
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	c.Header("Location", "/api/users/"+u.ID.String())
	c.JSON(http.StatusCreated, toResponse(u))
}

func (h handler) list(c *gin.Context) {
	var q struct {
		Page int `form:"page,default=0" binding:"min=0"`
		Size int `form:"size,default=20" binding:"min=1,max=100"`
	}
	if !bind(c, c.ShouldBindQuery(&q)) {
		return
	}
	page, err := h.svc.Users(c, q.Page, q.Size)
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]userResponse, 0, len(page.Items))
	for _, u := range page.Items {
		items = append(items, toResponse(u))
	}
	c.JSON(http.StatusOK, pageResponse{items, page.Page, page.Size, page.Total})
}

func (h handler) get(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	u, err := h.svc.User(c, id)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(u))
}

func (h handler) update(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateRequest
	if !bind(c, c.ShouldBindJSON(&req)) {
		return
	}
	u, err := h.svc.Rename(c, id, req.Name)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toResponse(u))
}

func (h handler) delete(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.svc.Delete(c, id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// welcome é assíncrono: grava no outbox e responde 202; o envio acontece via Kafka.
func (h handler) welcome(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if err := h.svc.RequestWelcome(c, id); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}

func (h handler) login(c *gin.Context) {
	var req loginRequest
	if !bind(c, c.ShouldBindJSON(&req)) {
		return
	}
	tokens, err := h.svc.Login(c, req.Email, req.Password)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toTokenResponse(tokens))
}

func (h handler) refresh(c *gin.Context) {
	var req refreshRequest
	if !bind(c, c.ShouldBindJSON(&req)) {
		return
	}
	tokens, err := h.svc.Refresh(c, req.RefreshToken)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, toTokenResponse(tokens))
}

func (h handler) logout(c *gin.Context) {
	var req refreshRequest
	if !bind(c, c.ShouldBindJSON(&req)) {
		return
	}
	if err := h.svc.Logout(c, req.RefreshToken); err != nil {
		fail(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// --- autenticação e autorização ---

const principalKey = "principal"

func authenticate(tokens security.JWT) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		p, err := tokens.Verify(raw)
		if !ok || err != nil {
			c.Header("WWW-Authenticate", "Bearer")
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Set(principalKey, p)
	}
}

func principal(c *gin.Context) security.Principal {
	return c.MustGet(principalKey).(security.Principal)
}

func requireRole(role user.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !slices.Contains(principal(c).Roles, role) {
			c.AbortWithStatus(http.StatusForbidden)
		}
	}
}

// selfOrAdmin libera ADMIN ou o próprio usuário do :id. Roda antes de validar o id,
// então um id malformado de outra pessoa dá 403, não 400.
func selfOrAdmin(c *gin.Context) {
	p := principal(c)
	if p.Subject != c.Param("id") && !slices.Contains(p.Roles, user.RoleAdmin) {
		c.AbortWithStatus(http.StatusForbidden)
	}
}

// --- erros ---

func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		problem(c, http.StatusBadRequest, "UserId inválido: "+c.Param("id"))
		return uuid.UUID{}, false
	}
	return id, true
}

func bind(c *gin.Context, err error) bool {
	if err != nil {
		problem(c, http.StatusBadRequest, "Requisição inválida")
		return false
	}
	return true
}

func fail(c *gin.Context, err error) {
	if v, ok := errors.AsType[*user.ValidationError](err); ok {
		problem(c, http.StatusBadRequest, v.Msg)
		return
	}
	switch {
	case errors.Is(err, user.ErrNotFound):
		c.Status(http.StatusNotFound)
	case errors.Is(err, user.ErrInvalidCredentials):
		problem(c, http.StatusUnauthorized, "Credenciais inválidas")
	case errors.Is(err, user.ErrInvalidRefresh):
		problem(c, http.StatusUnauthorized, "Refresh token inválido ou expirado")
	default:
		slog.ErrorContext(c, "erro inesperado", "method", c.Request.Method, "path", c.FullPath(), "err", err)
		problem(c, http.StatusInternalServerError, "Erro interno")
	}
}

func problem(c *gin.Context, status int, detail string) {
	c.Header("Content-Type", "application/problem+json")
	c.JSON(status, gin.H{
		"type": "about:blank", "title": http.StatusText(status), "status": status,
		"detail": detail, "instance": c.Request.URL.Path,
	})
}
