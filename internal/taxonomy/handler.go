package taxonomy

import (
	"net/http"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Taxonomy changes rarely and is read on nearly every page, so public responses are cached at
// the CDN for 10 minutes and served stale for a day while refreshing; the category.changed and
// tag.changed events purge them sooner.
const (
	publicMaxAge = 10 * time.Minute
	publicStale  = 24 * time.Hour
)

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/public/categories", httpx.HandlerFunc(h.roots))
	mux.Handle("GET /v1/public/categories/{path...}", httpx.HandlerFunc(h.byPath))
	mux.Handle("GET /v1/public/tags", httpx.HandlerFunc(h.listTags))
	mux.Handle("GET /v1/public/tags/search", httpx.HandlerFunc(h.searchTags))
	mux.Handle("GET /v1/public/tags/{kind}/{slug}", httpx.HandlerFunc(h.tag))

	mux.Handle("POST /v1/admin/categories", authed(httpx.HandlerFunc(h.createCategory)))
	mux.Handle("PATCH /v1/admin/categories/{id}", authed(httpx.HandlerFunc(h.updateCategory)))
	mux.Handle("POST /v1/admin/categories/{id}/move", authed(httpx.HandlerFunc(h.moveCategory)))
	mux.Handle("DELETE /v1/admin/categories/{id}", authed(httpx.HandlerFunc(h.deleteCategory)))
	mux.Handle("POST /v1/admin/tags", authed(httpx.HandlerFunc(h.createTag)))
	mux.Handle("PATCH /v1/admin/tags/{id}", authed(httpx.HandlerFunc(h.updateTag)))
	mux.Handle("DELETE /v1/admin/tags/{id}", authed(httpx.HandlerFunc(h.deleteTag)))
}

func (h *Handler) roots(w http.ResponseWriter, r *http.Request) error {
	cats, err := h.svc.Roots(r.Context())
	if err != nil {
		return err
	}
	httpx.Public(w, publicMaxAge, publicStale, "categories")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": cats})
	return nil
}

func (h *Handler) byPath(w http.ResponseWriter, r *http.Request) error {
	view, err := h.svc.ByPath(r.Context(), r.PathValue("path"))
	if err != nil {
		return err
	}
	httpx.Public(w, publicMaxAge, publicStale, "categories")
	httpx.JSON(w, http.StatusOK, view)
	return nil
}

func (h *Handler) listTags(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[TagCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.ListTags(r.Context(), TagKind(r.URL.Query().Get("kind")), req)
	if err != nil {
		return err
	}
	httpx.Public(w, publicMaxAge, publicStale, "tags")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) searchTags(w http.ResponseWriter, r *http.Request) error {
	var kind *TagKind
	if k := r.URL.Query().Get("kind"); k != "" {
		tk := TagKind(k)
		kind = &tk
	}
	tags, err := h.svc.SearchTags(r.Context(), r.URL.Query().Get("q"), kind)
	if err != nil {
		return err
	}
	httpx.Public(w, time.Minute, time.Hour, "tags")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": tags})
	return nil
}

func (h *Handler) tag(w http.ResponseWriter, r *http.Request) error {
	t, err := h.svc.TagBySlug(r.Context(), TagKind(r.PathValue("kind")), r.PathValue("slug"))
	if err != nil {
		return err
	}
	httpx.Public(w, publicMaxAge, publicStale, "tags")
	httpx.JSON(w, http.StatusOK, t)
	return nil
}

func (h *Handler) createCategory(w http.ResponseWriter, r *http.Request) error {
	var in CreateCategory
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	c, err := h.svc.Create(r.Context(), p, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, c)
	return nil
}

func (h *Handler) updateCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in UpdateCategory
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	c, err := h.svc.Update(r.Context(), p, id, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) moveCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in MoveCategory
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	c, err := h.svc.Move(r.Context(), p, id, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) deleteCategory(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	if err := h.svc.Delete(r.Context(), p, id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) createTag(w http.ResponseWriter, r *http.Request) error {
	var in CreateTag
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	t, err := h.svc.CreateTag(r.Context(), p, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, t)
	return nil
}

func (h *Handler) updateTag(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in UpdateTag
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	t, err := h.svc.UpdateTag(r.Context(), p, id, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, t)
	return nil
}

func (h *Handler) deleteTag(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	if err := h.svc.DeleteTag(r.Context(), p, id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}
