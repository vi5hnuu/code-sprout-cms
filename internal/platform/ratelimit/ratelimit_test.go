package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code-sprout-cms/internal/platform/testenv"
)

func TestAllowEnforcesBurstThenRefills(t *testing.T) {
	l := New(testenv.Redis(t))
	ctx := context.Background()
	key := fmt.Sprintf("test:%d", time.Now().UnixNano())
	// 10 per second with a burst of 3: three immediate requests pass, the fourth waits ~100ms.
	lim := Limit{Rate: 10, Period: time.Second, Burst: 3}

	for i := range 3 {
		ok, _, err := l.Allow(ctx, key, lim)
		if err != nil || !ok {
			t.Fatalf("request %d within burst rejected (err=%v)", i+1, err)
		}
	}
	ok, retry, err := l.Allow(ctx, key, lim)
	if err != nil || ok {
		t.Fatalf("request beyond burst allowed (err=%v)", err)
	}
	if retry <= 0 || retry > 100*time.Millisecond {
		t.Fatalf("retry-after = %v, want (0, 100ms]", retry)
	}

	time.Sleep(retry + 5*time.Millisecond)
	if ok, _, err := l.Allow(ctx, key, lim); err != nil || !ok {
		t.Fatalf("request after refill rejected (err=%v)", err)
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req.Header.Set("CF-Connecting-IP", "203.0.113.7")

	if got := ClientIPFrom("", "")(req); got != "10.0.0.1" {
		t.Fatalf("untrusted header must be ignored, got %q", got)
	}
	if got := ClientIPFrom("CF-Connecting-IP", "")(req); got != "203.0.113.7" {
		t.Fatalf("trusted header not used, got %q", got)
	}
	req.Header.Set("CF-Connecting-IP", "not-an-ip")
	if got := ClientIPFrom("CF-Connecting-IP", "")(req); got != "10.0.0.1" {
		t.Fatalf("garbage header must fall back to peer, got %q", got)
	}
}

func TestClientIPFromInternalProxy(t *testing.T) {
	resolve := ClientIPFrom("", "s3cret")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.9:5000" // the web server
	req.Header.Set(ClientIPHeader, "198.51.100.4")

	if got := resolve(req); got != "10.0.0.9" {
		t.Fatalf("client IP without proof must be ignored, got %q", got)
	}
	req.Header.Set(InternalProxyHeader, "wrong")
	if got := resolve(req); got != "10.0.0.9" {
		t.Fatalf("wrong proof must be ignored, got %q", got)
	}
	req.Header.Set(InternalProxyHeader, "s3cret")
	if got := resolve(req); got != "198.51.100.4" {
		t.Fatalf("proven request must use the relayed client IP, got %q", got)
	}
	req.Header.Del(ClientIPHeader)
	if got := resolve(req); got != "" {
		t.Fatalf("proven request without a client is unattributed, got %q", got)
	}
	if got := ClientIPFrom("", "")(req); got != "10.0.0.9" {
		t.Fatalf("with no secret configured the proof header means nothing, got %q", got)
	}
}

func TestMiddlewareSkipsUnattributedServerTraffic(t *testing.T) {
	l := New(testenv.Redis(t))
	name := fmt.Sprintf("test-unattributed-%d", time.Now().UnixNano())
	h := l.Middleware(name, Limit{Rate: 1, Period: time.Hour, Burst: 1}, func(*http.Request) string { return "" })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i := range 5 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d from unattributed server traffic was limited (%d)", i+1, rec.Code)
		}
	}
}

func TestAddressKeyGroupsIPv6By56(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		"2001:db8:1:2:aaaa::1": "2001:db8:1::/56",
		"2001:db8:1:2:ffff::9": "2001:db8:1::/56",
		"2001:db8:1:ff00::1":   "2001:db8:1:ff00::/56",
		"::ffff:203.0.113.7":   "::ffff:203.0.113.7",
		"not-an-address":       "not-an-address",
	}
	for in, want := range cases {
		if got := addressKey(in); got != want {
			t.Errorf("addressKey(%q) = %q, want %q", in, got, want)
		}
	}
}
