// Package account handles the reader's account as a whole. Today that is erasure: a reader
// can delete their account from any client (an app-store requirement as much as a right).
//
// Each domain erases its own tables (engagement, library, submissions, collections, roles,
// the user row); this package only runs them together in one transaction, so an erase either
// completes or leaves nothing half-done. Personal data is deleted; pages the reader authored
// stay, credited to "Former member".
package account

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
)

// EraseFunc deletes or anonymises one domain's data for a user inside the given transaction.
type EraseFunc func(ctx context.Context, tx pgx.Tx, userID int64) error

type Service struct {
	db      *db.DB
	erasers []EraseFunc
	// forget drops cached identity for the erased subject once the erase has committed.
	forget func(sub string)
}

func NewService(d *db.DB, forget func(sub string), erasers ...EraseFunc) *Service {
	return &Service{db: d, erasers: erasers, forget: forget}
}

// Erase deletes everything personal the CMS holds for the caller. The auth account is the
// client's to delete next (with the same session); until it is, the caller can still sign in
// and will find an empty, fresh account.
func (s *Service) Erase(ctx context.Context, p auth.Principal) error {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		for _, erase := range s.erasers {
			if err := erase(ctx, tx, p.UserID); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "account.erase", Entity: "user", EntityID: p.UserID})
	})
	if err != nil {
		return err
	}
	s.forget(p.Subject)
	return nil
}

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers DELETE /v1/me. `authed` wraps handlers that need a signed-in caller.
func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("DELETE /v1/me", authed(httpx.HandlerFunc(h.erase)))
}

func (h *Handler) erase(w http.ResponseWriter, r *http.Request) error {
	p, _ := auth.FromContext(r.Context())
	if err := h.svc.Erase(r.Context(), p); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
