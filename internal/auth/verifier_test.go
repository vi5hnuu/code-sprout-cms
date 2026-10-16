package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testIssuer   = "https://auth.test"
	testAudience = "code-sprout-cms"
	testKID      = "k1"
)

// fakeAuth serves a JWKS like the real auth service and mints tokens shaped like its
// TokenService output.
type fakeAuth struct {
	key *rsa.PrivateKey
	srv *httptest.Server
}

func newFakeAuth(t *testing.T) *fakeAuth {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": testKID,
		"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
	}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	}))
	t.Cleanup(srv.Close)
	return &fakeAuth{key: key, srv: srv}
}

func (f *fakeAuth) token(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"sub": "USR123", "iss": testIssuer, "aud": testAudience,
		"iat": now.Unix(), "exp": now.Add(15 * time.Minute).Unix(),
		"email": "a@b.c", "username": "asha", "roles": []string{"ROLE_USER"},
		"account_type": "USER", "is_verified": true, "token_type": "access",
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = testKID
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newVerifier(t *testing.T, f *fakeAuth) *JWKSVerifier {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	v, err := NewJWKSVerifier(ctx, f.srv.URL, testIssuer, testAudience)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerify(t *testing.T) {
	f := newFakeAuth(t)
	v := newVerifier(t, f)

	cases := []struct {
		name   string
		mutate func(jwt.MapClaims)
		ok     bool
	}{
		{"valid", nil, true},
		{"admin role", func(c jwt.MapClaims) { c["roles"] = []string{"ROLE_USER", "ROLE_ADMIN"} }, true},
		{"wrong audience", func(c jwt.MapClaims) { c["aud"] = "pdf-studio-api" }, false},
		{"wrong issuer", func(c jwt.MapClaims) { c["iss"] = "https://evil.test" }, false},
		{"expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }, false},
		{"missing exp", func(c jwt.MapClaims) { delete(c, "exp") }, false},
		{"not an access token", func(c jwt.MapClaims) { c["token_type"] = "refresh" }, false},
		{"missing sub", func(c jwt.MapClaims) { delete(c, "sub") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := v.Verify(context.Background(), f.token(t, tc.mutate))
			if tc.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want rejection, got valid")
			}
			if tc.ok && claims.Subject != "USR123" {
				t.Fatalf("subject = %q", claims.Subject)
			}
		})
	}
}

func TestVerifyRejectsForeignKeyAndAlgorithms(t *testing.T) {
	f := newFakeAuth(t)
	v := newVerifier(t, f)

	// Signed by a key auth never published.
	other := newFakeAuth(t)
	if _, err := v.Verify(context.Background(), other.token(t, nil)); err == nil {
		t.Fatal("token from an unknown key was accepted")
	}

	// alg=none and HMAC tokens must never be accepted, whatever they claim.
	none := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"sub": "x", "iss": testIssuer, "aud": testAudience, "exp": time.Now().Add(time.Hour).Unix(), "token_type": "access"})
	s, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := v.Verify(context.Background(), s); err == nil {
		t.Fatal("alg=none accepted")
	}
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "x", "iss": testIssuer, "aud": testAudience, "exp": time.Now().Add(time.Hour).Unix(), "token_type": "access"})
	s, _ = hs.SignedString([]byte("secret"))
	if _, err := v.Verify(context.Background(), s); err == nil {
		t.Fatal("HS256 accepted")
	}
}

func TestClaimsIsAdmin(t *testing.T) {
	if (Claims{Roles: []string{"ROLE_USER"}}).IsAdmin() {
		t.Fatal("user reported as admin")
	}
	if !(Claims{Roles: []string{"ROLE_ADMIN"}}).IsAdmin() {
		t.Fatal("admin not detected")
	}
}
