package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code-sprout-cms/internal/platform/apperr"
)

func TestDecode(t *testing.T) {
	type body struct {
		Title string `json:"title"`
	}
	cases := []struct {
		name, ct, payload, wantCode string
	}{
		{"valid", "application/json", `{"title":"x"}`, ""},
		{"charset param ok", "application/json; charset=utf-8", `{"title":"x"}`, ""},
		{"wrong content type", "text/plain", `{"title":"x"}`, "request.content_type"},
		{"unknown field", "application/json", `{"titel":"x"}`, "request.malformed_json"},
		{"trailing data", "application/json", `{"title":"x"}{}`, "request.malformed_json"},
		{"too large", "application/json", `{"title":"` + strings.Repeat("a", MaxJSONBody) + `"}`, "request.too_large"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", tc.ct)
			var b body
			err := Decode(httptest.NewRecorder(), req, &b)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			e, ok := apperr.As(err)
			if !ok || e.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %s", err, tc.wantCode)
			}
		})
	}
}

func TestErrorRendering(t *testing.T) {
	render := func(err error) (int, Problem) {
		rec := httptest.NewRecorder()
		Error(rec, httptest.NewRequest(http.MethodGet, "/x", nil), err)
		var p Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("content-type = %q", ct)
		}
		return rec.Code, p
	}

	code, p := render(apperr.ValidationFailed(map[string]string{"slug": "taken"}))
	if code != 400 || p.Code != "validation_failed" || p.Errors["slug"] != "taken" {
		t.Fatalf("validation: %d %+v", code, p)
	}
	code, p = render(apperr.PreconditionFailed("content.stale_revision", "Reload."))
	if code != 412 || p.Code != "content.stale_revision" {
		t.Fatalf("precondition: %d %+v", code, p)
	}
	// Internal errors must not leak their message.
	code, p = render(errors.New("pq: password authentication failed for user sprout"))
	if code != 500 || strings.Contains(p.Detail, "password") {
		t.Fatalf("internal error leaked: %d %+v", code, p)
	}
}
