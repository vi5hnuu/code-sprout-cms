package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubVerifier struct {
	claims Claims
	err    error
}

func (s stubVerifier) Verify(context.Context, string) (Claims, error) { return s.claims, s.err }

type stubUsers struct{ id int64 }

func (s stubUsers) ResolveID(context.Context, string, string) (int64, error) { return s.id, nil }

func serve(t *testing.T, v Verifier, header string, inner http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	Authenticate(v, stubUsers{id: 42})(inner).ServeHTTP(rec, req)
	return rec
}

func TestAuthenticate(t *testing.T) {
	var got *Principal
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := FromContext(r.Context()); ok {
			got = &p
		}
		w.WriteHeader(http.StatusOK)
	})
	valid := stubVerifier{claims: Claims{Roles: []string{"ROLE_ADMIN"}, AccountType: "USER"}}
	valid.claims.Subject = "USR1"

	t.Run("no token is anonymous", func(t *testing.T) {
		got = nil
		if rec := serve(t, valid, "", capture); rec.Code != http.StatusOK || got != nil {
			t.Fatalf("code=%d principal=%v", rec.Code, got)
		}
	})
	t.Run("valid token attaches principal", func(t *testing.T) {
		got = nil
		rec := serve(t, valid, "Bearer abc", capture)
		if rec.Code != http.StatusOK || got == nil {
			t.Fatalf("code=%d principal=%v", rec.Code, got)
		}
		if got.UserID != 42 || got.Subject != "USR1" || !got.IsAdmin || got.IsGuest {
			t.Fatalf("principal = %+v", *got)
		}
	})
	t.Run("lowercase scheme accepted", func(t *testing.T) {
		if rec := serve(t, valid, "bearer abc", capture); rec.Code != http.StatusOK {
			t.Fatalf("code=%d", rec.Code)
		}
	})
	t.Run("invalid token is 401, not anonymous", func(t *testing.T) {
		rec := serve(t, stubVerifier{err: errors.New("bad")}, "Bearer abc", capture)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("content-type=%q", ct)
		}
	})
}

func TestRequireUser(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	RequireUser(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: code=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	RequireUser(ok).ServeHTTP(rec, req.WithContext(WithPrincipal(req.Context(), Principal{UserID: 1})))
	if rec.Code != http.StatusOK {
		t.Fatalf("signed in: code=%d", rec.Code)
	}
}
