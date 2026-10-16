// Package auth turns access tokens issued by the shared auth service (IdeaProjects/auth) into
// a request Principal. This service never sees passwords or refresh tokens: it verifies the
// RS256 signature locally against auth's published JWKS, so authenticating a request costs no
// network call (keys are cached and refreshed in the background, and on an unknown kid).
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

// Claims mirrors what auth's TokenService puts in an access token.
type Claims struct {
	jwt.RegisteredClaims
	Email       string   `json:"email"`
	Username    string   `json:"username"`
	Roles       []string `json:"roles"`
	AccountType string   `json:"account_type"` // USER | GUEST
	Verified    bool     `json:"is_verified"`
	TokenType   string   `json:"token_type"`
}

const (
	accessTokenType = "access"
	authAdminRole   = "ROLE_ADMIN"
)

func (c Claims) IsAdmin() bool { return slices.Contains(c.Roles, authAdminRole) }

// Verifier validates a raw bearer token. An interface so handlers and middleware can be tested
// without a running auth service.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (Claims, error)
}

var ErrInvalidToken = errors.New("invalid access token")

type JWKSVerifier struct {
	keys   keyfunc.Keyfunc
	parser *jwt.Parser
}

// NewJWKSVerifier starts a background JWKS refresher bound to ctx. It returns without waiting
// for the first fetch, so the API can boot while auth is briefly unavailable; tokens simply
// fail verification until keys arrive.
func NewJWKSVerifier(ctx context.Context, jwksURL, issuer, audience string) (*JWKSVerifier, error) {
	bootWithoutKeys := true
	keys, err := keyfunc.NewDefaultOverrideCtx(ctx, []string{jwksURL}, keyfunc.Override{
		HTTPTimeout:               5 * time.Second,
		NoErrorReturnFirstHTTPReq: &bootWithoutKeys,
		RefreshInterval:           15 * time.Minute,
		// A token signed by a key we have not seen triggers a refetch, at most once a minute,
		// so key rotation in auth takes effect at once without letting junk kids hammer it.
		RefreshUnknownKID: rate.NewLimiter(rate.Every(time.Minute), 1),
		RefreshErrorHandlerFunc: func(u string) func(context.Context, error) {
			return func(_ context.Context, err error) { slog.Warn("jwks refresh failed", "url", u, "err", err) }
		},
	})
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(30*time.Second), // tolerate small clock skew between hosts
	)
	return &JWKSVerifier{keys: keys, parser: parser}, nil
}

func (v *JWKSVerifier) Verify(_ context.Context, rawToken string) (Claims, error) {
	var claims Claims
	if _, err := v.parser.ParseWithClaims(rawToken, &claims, v.keys.Keyfunc); err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	// Refresh tokens are opaque in auth today, but checking the type keeps any future signed
	// non-access token from being accepted here.
	if claims.TokenType != accessTokenType {
		return Claims{}, fmt.Errorf("%w: token_type %q", ErrInvalidToken, claims.TokenType)
	}
	if claims.Subject == "" {
		return Claims{}, fmt.Errorf("%w: missing sub", ErrInvalidToken)
	}
	return claims, nil
}
