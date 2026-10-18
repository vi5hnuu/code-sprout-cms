package collection

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	me := func(pattern string, fn httpx.HandlerFunc) { mux.Handle(pattern, authed(fn)) }
	me("GET /v1/me/collections", h.mine)
	me("POST /v1/me/collections", h.create)
	me("PUT /v1/me/collections/{id}", h.update)
	me("DELETE /v1/me/collections/{id}", h.delete)
	me("PUT /v1/me/collections/{id}/items/{contentId}", h.addItem)
	me("DELETE /v1/me/collections/{id}/items/{contentId}", h.removeItem)
	me("PUT /v1/me/collections/{id}/order", h.reorder)

	me("GET /v1/studio/collections", h.editorial)

	mux.Handle("GET /v1/public/collections", httpx.HandlerFunc(h.publicEditorial))
	mux.Handle("GET /v1/public/collections/{kind}/{slug}", httpx.HandlerFunc(h.publicBySlug))
	mux.Handle("GET /v1/public/lists/{id}", httpx.HandlerFunc(h.view))
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

// mine lists the caller's lists; ?containing=<contentId> narrows it to the lists holding that
// item (for an "add to list" menu).
func (h *Handler) mine(w http.ResponseWriter, r *http.Request) error {
	cs, err := h.svc.Mine(r.Context(), principal(r))
	if err != nil {
		return err
	}
	if v := r.URL.Query().Get("containing"); v != "" {
		cid, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || cid <= 0 {
			return apperr.ValidationFailed(map[string]string{"containing": "containing must be a content id."})
		}
		holding, err := h.svc.Containing(r.Context(), principal(r), cid)
		if err != nil {
			return err
		}
		keep := make(map[int64]bool, len(holding))
		for _, id := range holding {
			keep[id] = true
		}
		filtered := cs[:0]
		for _, c := range cs {
			if keep[c.ID] {
				filtered = append(filtered, c)
			}
		}
		cs = filtered
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": cs})
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	c, err := h.svc.Create(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, c)
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in Input
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	c, err := h.svc.Update(r.Context(), principal(r), id, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.Delete(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) addItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	cid, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	if err := h.svc.AddItem(r.Context(), principal(r), id, cid); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) removeItem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	cid, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	if err := h.svc.RemoveItem(r.Context(), principal(r), id, cid); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) reorder(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		ContentIDs []int64 `json:"contentIds"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.Reorder(r.Context(), principal(r), id, in.ContentIDs); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) publicBySlug(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[float64](r)
	if err != nil {
		return err
	}
	v, err := h.svc.PublicBySlug(r.Context(), Kind(strings.ToUpper(r.PathValue("kind"))), r.PathValue("slug"), req)
	if err != nil {
		return err
	}
	httpx.Public(w, 5*time.Minute, 24*time.Hour, "listings")
	httpx.JSON(w, http.StatusOK, v)
	return nil
}

// publicEditorial lists public study plans, series or courses: ?kind=study_plan|series|course.
func (h *Handler) publicEditorial(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[EditorialCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.PublicEditorial(r.Context(), Kind(strings.ToUpper(r.URL.Query().Get("kind"))), req)
	if err != nil {
		return err
	}
	httpx.Public(w, 5*time.Minute, 24*time.Hour, "listings")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// view serves reader lists. Owners see their private lists too, so responses are never cached
// publicly for signed-in callers.
func (h *Handler) view(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	req, err := page.Parse[float64](r)
	if err != nil {
		return err
	}
	var p *auth.Principal
	if pr, ok := auth.FromContext(r.Context()); ok {
		p = &pr
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		httpx.Public(w, time.Minute, time.Hour, "listings")
	}
	v, err := h.svc.View(r.Context(), p, id, req)
	if err != nil {
		w.Header().Del("Cache-Control")
		return err
	}
	httpx.JSON(w, http.StatusOK, v)
	return nil
}

func (h *Handler) editorial(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[EditorialCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Editorial(r.Context(), principal(r), Kind(r.URL.Query().Get("kind")), req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}
