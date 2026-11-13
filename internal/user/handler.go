package user

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers user endpoints. `authed` wraps handlers that need a signed-in user.
func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/me", authed(httpx.HandlerFunc(h.me)))
	mux.Handle("PUT /v1/me/profile", authed(httpx.HandlerFunc(h.updateProfile)))
	mux.Handle("PUT /v1/me/preferences", authed(httpx.HandlerFunc(h.setPreferences)))
	mux.Handle("GET /v1/public/users/{handle}", httpx.HandlerFunc(h.byHandle))
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) error {
	var in UpdateProfile
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	profile, err := h.svc.UpdateProfile(r.Context(), p.UserID, in)
	if err != nil {
		return err
	}
	writeMe(w, p, profile)
	return nil
}

func (h *Handler) setPreferences(w http.ResponseWriter, r *http.Request) error {
	var in Preferences
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	profile, err := h.svc.SetPreferences(r.Context(), p.UserID, in)
	if err != nil {
		return err
	}
	writeMe(w, p, profile)
	return nil
}

func (h *Handler) byHandle(w http.ResponseWriter, r *http.Request) error {
	u, err := h.svc.ByHandle(r.Context(), strings.ToLower(r.PathValue("handle")))
	if err != nil {
		return err
	}
	httpx.Public(w, 10*time.Minute, 24*time.Hour, "user:"+strconv.FormatInt(u.ID, 10))
	httpx.JSON(w, http.StatusOK, u)
	return nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	p, _ := auth.FromContext(r.Context())
	profile, err := h.svc.Profile(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeMe(w, p, profile)
	return nil
}

// writeMe answers with the signed-in reader as the API describes them (the Me schema): the
// profile plus what their token says about them. Every /v1/me endpoint returns this shape.
func writeMe(w http.ResponseWriter, p auth.Principal, profile Profile) {
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, struct {
		Profile
		IsAdmin bool `json:"isAdmin"`
		IsGuest bool `json:"isGuest"`
	}{profile, p.IsAdmin, p.IsGuest})
}
