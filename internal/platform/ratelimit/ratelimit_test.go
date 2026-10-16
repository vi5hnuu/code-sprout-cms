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

	if got := ClientIPFrom("")(req); got != "10.0.0.1" {
		t.Fatalf("untrusted header must be ignored, got %q", got)
	}
	if got := ClientIPFrom("CF-Connecting-IP")(req); got != "203.0.113.7" {
		t.Fatalf("trusted header not used, got %q", got)
	}
	req.Header.Set("CF-Connecting-IP", "not-an-ip")
	if got := ClientIPFrom("CF-Connecting-IP")(req); got != "10.0.0.1" {
		t.Fatalf("garbage header must fall back to peer, got %q", got)
	}
}
