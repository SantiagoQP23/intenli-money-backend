package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	googleIssuer  = "https://accounts.google.com"
	googleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"
)

// GoogleIdentity is what we trust from a verified Google ID token.
type GoogleIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// GoogleVerifier checks a Google ID token sent by the app.
type GoogleVerifier interface {
	Verify(ctx context.Context, idToken string) (GoogleIdentity, error)
}

var ErrGoogleNotConfigured = errors.New("google sign-in is not configured")

type googleVerifier struct {
	clientIDs []string
	verifier  *oidc.IDTokenVerifier
}

// NewGoogleVerifier accepts tokens issued to any of clientIDs (the iOS,
// Android and web OAuth clients). Google's signing keys are fetched lazily
// and cached.
func NewGoogleVerifier(clientIDs []string) GoogleVerifier {
	if len(clientIDs) == 0 {
		return disabledGoogleVerifier{}
	}
	keys := oidc.NewRemoteKeySet(context.Background(), googleJWKSURL)
	return &googleVerifier{
		clientIDs: clientIDs,
		// The audience is checked below against every allowed client ID.
		verifier: oidc.NewVerifier(googleIssuer, keys, &oidc.Config{SkipClientIDCheck: true}),
	}
}

func (g *googleVerifier) Verify(ctx context.Context, idToken string) (GoogleIdentity, error) {
	tok, err := g.verifier.Verify(ctx, idToken)
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !slices.ContainsFunc(tok.Audience, func(aud string) bool { return slices.Contains(g.clientIDs, aud) }) {
		return GoogleIdentity{}, fmt.Errorf("%w: unexpected audience", ErrInvalidToken)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := tok.Claims(&claims); err != nil {
		return GoogleIdentity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if claims.Email == "" {
		return GoogleIdentity{}, fmt.Errorf("%w: token has no email", ErrInvalidToken)
	}
	return GoogleIdentity{
		Subject:       tok.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
	}, nil
}

type disabledGoogleVerifier struct{}

func (disabledGoogleVerifier) Verify(context.Context, string) (GoogleIdentity, error) {
	return GoogleIdentity{}, ErrGoogleNotConfigured
}
