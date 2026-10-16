// Package httpx holds the HTTP plumbing shared by every handler: JSON in/out, RFC 9457 error
// rendering, and middleware. Handlers stay thin: decode → call service → respond.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"code-sprout-cms/internal/platform/apperr"
)

// MaxJSONBody caps request bodies. Article bodies are the largest JSON payloads we accept;
// files never travel through the API (presigned uploads), so 2 MiB is generous.
const MaxJSONBody = 2 << 20

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("write json response", "err", err)
	}
}

func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// Decode reads a single JSON object into dst, rejecting unknown fields, trailing data and
// oversize bodies, so a typo in a client payload fails loudly instead of being ignored.
func Decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return apperr.Invalid("request.content_type", "Content-Type must be application/json.")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return apperr.Invalid("request.too_large", fmt.Sprintf("Request body exceeds %d bytes.", maxErr.Limit))
		}
		return apperr.Invalid("request.malformed_json", "Request body is not valid JSON: "+err.Error())
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return apperr.Invalid("request.malformed_json", "Request body must contain a single JSON object.")
	}
	return nil
}

// Problem is an RFC 9457 problem-details body.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Code      string            `json:"code"`
	Errors    map[string]string `json:"errors,omitempty"`
	RequestID string            `json:"requestId,omitempty"`
}

var kindStatus = map[apperr.Kind]int{
	apperr.KindInvalid:            http.StatusBadRequest,
	apperr.KindUnauthenticated:    http.StatusUnauthorized,
	apperr.KindForbidden:          http.StatusForbidden,
	apperr.KindNotFound:           http.StatusNotFound,
	apperr.KindConflict:           http.StatusConflict,
	apperr.KindPreconditionFailed: http.StatusPreconditionFailed,
	apperr.KindRateLimited:        http.StatusTooManyRequests,
}

// Error renders err as problem+json. Domain errors keep their code and detail; anything else
// is logged with the request id and reported as a generic 500 so internals never leak.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	reqID := RequestID(r.Context())
	p := Problem{Type: "about:blank", RequestID: reqID}

	if e, ok := apperr.As(err); ok && e.Kind != apperr.KindInternal {
		p.Status = kindStatus[e.Kind]
		p.Code, p.Detail, p.Errors = e.Code, e.Detail, e.Fields
		if e.Err != nil {
			slog.Debug("request failed", "request_id", reqID, "code", e.Code, "err", e.Err)
		}
	} else {
		p.Status = http.StatusInternalServerError
		p.Code, p.Detail = "internal", "Something went wrong. Please try again."
		slog.Error("request failed", "request_id", reqID, "method", r.Method, "path", r.URL.Path, "err", err)
	}
	p.Title = http.StatusText(p.Status)

	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// HandlerFunc is an http.HandlerFunc that returns an error instead of writing one, so handlers
// read top to bottom without repeating error rendering on every branch.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func (h HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h(w, r); err != nil {
		Error(w, r, err)
	}
}
