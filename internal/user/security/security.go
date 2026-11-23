// Package security implementa Hasher (bcrypt) e AccessTokens (JWT HS256) de user,
// e verifica o JWT para o middleware HTTP.
package security

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/dirceurezende/user-template-go/internal/user"
)

type Bcrypt struct{ Cost int } // 0 = bcrypt.DefaultCost

func (b Bcrypt) Hash(raw string) (user.PasswordHash, error) {
	cost := b.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	h, err := bcrypt.GenerateFromPassword([]byte(raw), cost)
	return user.PasswordHash(h), err
}

func (Bcrypt) Matches(hash user.PasswordHash, raw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(raw)) == nil
}

// JWT emite e verifica access tokens: sub = id do usuário, roles = papéis.
type JWT struct {
	Secret []byte
	TTL    time.Duration
	Now    func() time.Time // nil = time.Now
}

const MinSecretBytes = 32

type claims struct {
	Roles []user.Role `json:"roles"`
	jwt.RegisteredClaims
}

func (j JWT) now() time.Time {
	if j.Now == nil {
		return time.Now()
	}
	return j.Now()
}

func (j JWT) Issue(u user.User) (string, time.Duration, error) {
	now := j.now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		Roles: u.Roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.TTL)),
		},
	})
	s, err := token.SignedString(j.Secret)
	return s, j.TTL, err
}

// Principal é quem está autenticado na requisição.
type Principal struct {
	Subject string
	Roles   []user.Role
}

var ErrInvalidToken = errors.New("token inválido")

func (j JWT) Verify(token string) (Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return j.Secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(j.now))
	if err != nil || c.Subject == "" {
		return Principal{}, ErrInvalidToken
	}
	return Principal{Subject: c.Subject, Roles: c.Roles}, nil
}
