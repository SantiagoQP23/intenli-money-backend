package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const accessTokenIssuer = "intenli-money"

var ErrInvalidToken = errors.New("invalid token")

// AccessTokens issues and verifies short-lived HS256 JWT access tokens.
type AccessTokens struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewAccessTokens(secret []byte, ttl time.Duration) *AccessTokens {
	return &AccessTokens{secret: secret, ttl: ttl, now: time.Now}
}

func (a *AccessTokens) TTL() time.Duration { return a.ttl }

// Issue returns a signed access token for the user.
func (a *AccessTokens) Issue(userID uuid.UUID) (string, error) {
	now := a.now()
	claims := jwt.RegisteredClaims{
		Issuer:    accessTokenIssuer,
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(a.ttl)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.secret)
}

// Verify checks the signature, issuer and expiry and returns the user ID.
func (a *AccessTokens) Verify(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims,
		func(*jwt.Token) (any, error) { return a.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(accessTokenIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(a.now),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}
	return id, nil
}

// NewOpaqueToken returns a random URL-safe token and its SHA-256 hash.
// Only the hash is stored; the token is shown to the client once.
func NewOpaqueToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashOpaqueToken(token), nil
}

func HashOpaqueToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
