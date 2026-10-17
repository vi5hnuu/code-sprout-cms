// Package apperr is the error vocabulary shared by every domain package. Services return these
// without knowing about HTTP; the transport layer (httpx) maps Kind to a status code and renders
// RFC 9457 problem details. Anything that is not an *Error is treated as an internal failure
// and its message is never shown to clients.
package apperr

import (
	"errors"
	"fmt"
)

type Kind int

const (
	KindInternal Kind = iota
	KindInvalid
	KindUnauthenticated
	KindForbidden
	KindNotFound
	KindConflict
	KindPreconditionFailed   // stale If-Match: someone else saved first
	KindPreconditionRequired // If-Match missing on a write that needs it
	KindRateLimited
)

type Error struct {
	Kind Kind
	// Code is a stable, machine-readable identifier (e.g. "content.slug_taken") that clients
	// may switch on; Detail is human-readable and may change wording at any time.
	Code   string
	Detail string
	Fields map[string]string // per-field validation messages, for KindInvalid
	// Details is structured, client-facing context (e.g. the Markdown issues blocking a
	// publish). Must be JSON-serialisable and safe to show.
	Details any
	Err     error // wrapped cause, logged but never exposed
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Detail, e.Err)
	}
	return e.Code + ": " + e.Detail
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, code, detail string) *Error {
	return &Error{Kind: kind, Code: code, Detail: detail}
}

func Invalid(code, detail string) *Error   { return New(KindInvalid, code, detail) }
func NotFound(code, detail string) *Error  { return New(KindNotFound, code, detail) }
func Conflict(code, detail string) *Error  { return New(KindConflict, code, detail) }
func Forbidden(code, detail string) *Error { return New(KindForbidden, code, detail) }
func Unauthenticated(detail string) *Error {
	return New(KindUnauthenticated, "auth.unauthenticated", detail)
}
func RateLimited(detail string) *Error { return New(KindRateLimited, "rate_limited", detail) }
func PreconditionFailed(code, detail string) *Error {
	return New(KindPreconditionFailed, code, detail)
}

// ValidationFailed builds a KindInvalid error carrying one message per offending field.
func ValidationFailed(fields map[string]string) *Error {
	return &Error{Kind: KindInvalid, Code: "validation_failed", Detail: "One or more fields are invalid.", Fields: fields}
}

// As extracts an *Error from err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}
