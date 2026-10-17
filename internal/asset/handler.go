package asset

import (
	"net/http"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers upload endpoints. createLimit throttles issuing upload URLs, the one call
// that lets a client put bytes into our storage.
func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, createLimit httpx.Middleware) {
	mux.Handle("POST /v1/studio/uploads", authed(createLimit(httpx.HandlerFunc(h.create))))
	mux.Handle("POST /v1/studio/uploads/{id}/complete", authed(httpx.HandlerFunc(h.complete)))
	mux.Handle("GET /v1/studio/uploads/{id}", authed(httpx.HandlerFunc(h.get)))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateUpload
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	ticket, err := h.svc.CreateUpload(r.Context(), p, in)
	if err != nil {
		return err
	}
	status := http.StatusCreated
	if ticket.AlreadyUploaded {
		status = http.StatusOK
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, status, ticket)
	return nil
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	f, err := h.svc.Complete(r.Context(), p, id)
	if err != nil {
		return err
	}
	status := http.StatusOK
	if f.Status == StatusVerifying {
		status = http.StatusAccepted // poll GET until READY
	}
	httpx.JSON(w, status, f)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	f, err := h.svc.Get(r.Context(), p, id)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, f)
	return nil
}
