package auth

import (
	"context"
	"net/http"
	"strings"

	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
)

// Principal is the authenticated caller attached to a request.
type Principal struct {
	UserID  int64  // app_user.id: what every CMS table references
	Subject string // auth service user id
	IsAdmin bool   // auth ROLE_ADMIN: CMS super-admin
	IsGuest bool   // auth GUEST account: may read and react, not author
}

// UserResolver maps an auth subject to the local user id (implemented by user.Service).
type UserResolver interface {
	ResolveID(ctx context.Context, sub, displayName string) (int64, error)
}

type ctxKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// WithPrincipal is exported for handler tests.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Authenticate attaches a Principal when the request carries a bearer token and lets
// anonymous requests through unchanged; routes that need a user add RequireUser.
//
// A present-but-invalid token is rejected with 401 rather than downgraded to anonymous: the
// client believes it is signed in, and a 401 is its cue to refresh, whereas silently serving
// anonymous data would show a logged-in user a logged-out page.
func Authenticate(v Verifier, users UserResolver) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			claims, err := v.Verify(r.Context(), raw)
			if err != nil {
				httpx.Error(w, r, &apperr.Error{
					Kind: apperr.KindUnauthenticated, Code: "auth.invalid_token",
					Detail: "The access token is invalid or expired.", Err: err,
				})
				return
			}
			id, err := users.ResolveID(r.Context(), claims.Subject, claims.Username)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			p := Principal{
				UserID:  id,
				Subject: claims.Subject,
				IsAdmin: claims.IsAdmin(),
				IsGuest: claims.AccountType == "GUEST",
			}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
		})
	}
}

// RequireUser rejects anonymous requests.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); !ok {
			httpx.Error(w, r, apperr.Unauthenticated("Sign in to continue."))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}
