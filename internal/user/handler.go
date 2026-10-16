package user

import (
	"net/http"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers user endpoints. `authed` wraps handlers that need a signed-in user.
func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/me", authed(httpx.HandlerFunc(h.me)))
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := auth.FromContext(r.Context())
	profile, err := h.svc.Profile(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, struct {
		Profile
		IsAdmin bool `json:"isAdmin"`
		IsGuest bool `json:"isGuest"`
	}{profile, p.IsAdmin, p.IsGuest})
	return nil
}
