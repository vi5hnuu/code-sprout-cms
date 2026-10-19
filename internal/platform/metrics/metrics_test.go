package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Requests are labelled by route pattern, so /items/1 and /items/2 are one series; paths that
// match nothing share a single "unmatched" series.
func TestHTTPLabelsByRoutePattern(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := m.HTTP(mux)(mux)
	for _, path := range []string{"/items/1", "/items/2", "/nope", "/also-nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	body := scrape(t, m)
	for _, want := range []string{
		`http_requests_total{code="418",method="GET",route="GET /items/{id}"} 2`,
		`http_requests_total{code="404",method="GET",route="unmatched"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "/items/1") {
		t.Error("a raw path leaked into a label")
	}
}

// A sample that fails is absent for that scrape, never reported as a misleading zero.
func TestSampledGaugesAreSkippedWhenTheReadFails(t *testing.T) {
	m := New()
	fail := true
	m.Sampled(func(context.Context) (map[string]float64, error) {
		if fail {
			return nil, errors.New("database down")
		}
		return map[string]float64{"queue_depth": 7}, nil
	}, map[string]string{"queue_depth": "Items waiting."})
	if n, _ := testutil.GatherAndCount(m.reg, "queue_depth"); n != 0 {
		t.Fatalf("failed read reported %d series", n)
	}
	fail = false
	if !strings.Contains(scrape(t, m), "queue_depth 7") {
		t.Error("successful read not reported")
	}
}

// Outbox outcomes are counted by topic and result.
func TestEventsCountOutcomes(t *testing.T) {
	m := New()
	e := m.Events()
	e.Observe("content.published", nil, 0)
	e.Observe("content.published", errors.New("search down"), 0)
	if got := testutil.ToFloat64(e.handled.WithLabelValues("content.published", "error")); got != 1 {
		t.Errorf("errors = %v", got)
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}
