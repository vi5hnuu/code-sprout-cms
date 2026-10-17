package content

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	handle := func(pattern string, fn httpx.HandlerFunc) { mux.Handle(pattern, authed(fn)) }

	handle("POST /v1/studio/content", h.create)
	handle("GET /v1/studio/content", h.listMine)
	handle("GET /v1/studio/review-queue", h.reviewQueue)
	handle("GET /v1/studio/content/{id}", h.get)
	handle("PUT /v1/studio/content/{id}/draft", h.saveDraft)
	handle("DELETE /v1/studio/content/{id}", h.delete)

	handle("GET /v1/studio/content/{id}/revisions", h.revisions)
	handle("GET /v1/studio/content/{id}/revisions/{rev}", h.revision)
	handle("POST /v1/studio/content/{id}/revisions/{rev}/restore", h.restore)
	handle("GET /v1/studio/content/{id}/reviews", h.reviews)

	handle("POST /v1/studio/content/{id}/submit", h.step((*Service).Submit))
	handle("POST /v1/studio/content/{id}/withdraw", h.step((*Service).Withdraw))
	handle("POST /v1/studio/content/{id}/publish", h.step((*Service).Publish))
	handle("POST /v1/studio/content/{id}/archive", h.step((*Service).Archive))
	handle("POST /v1/studio/content/{id}/review", h.review)
	handle("POST /v1/studio/content/{id}/schedule", h.schedule)
}

// writeDraft sends the studio view with its revision as the ETag, which the editor echoes
// back in If-Match on the next save.
func writeDraft(w http.ResponseWriter, status int, d Draft) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(int(d.DraftRevNo))))
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, status, d)
}

// ifMatch reads the revision number the client last saw. Draft writes require it: without
// it, two editors' saves would silently overwrite each other.
func ifMatch(r *http.Request) (int32, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		return 0, &apperr.Error{Kind: apperr.KindPreconditionRequired, Code: "content.if_match_required",
			Detail: "Send If-Match with the draft revision (the ETag you loaded)."}
	}
	n, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 32)
	if err != nil {
		return 0, apperr.Invalid("content.bad_if_match", "If-Match must be the draft ETag, e.g. \"12\".")
	}
	return int32(n), nil
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in CreateInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := h.svc.Create(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusCreated, d)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	d, err := h.svc.Get(r.Context(), principal(r), id)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusOK, d)
	return nil
}

func (h *Handler) saveDraft(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	rev, err := ifMatch(r)
	if err != nil {
		return err
	}
	var in DraftInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := h.svc.SaveDraft(r.Context(), principal(r), id, rev, in)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusOK, d)
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

func (h *Handler) listMine(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[ListCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.ListMine(r.Context(), principal(r), req)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reviewQueue(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[ListCursor](r)
	if err != nil {
		return err
	}
	state := ReviewState(r.URL.Query().Get("state"))
	if state == "" {
		state = ReviewInReview
	}
	res, err := h.svc.ReviewQueue(r.Context(), principal(r), state, req)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) revisions(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	req, err := page.Parse[int32](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Revisions(r.Context(), principal(r), id, req)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func revParam(r *http.Request) (int32, error) {
	n, err := strconv.ParseInt(r.PathValue("rev"), 10, 32)
	if err != nil || n <= 0 {
		return 0, apperr.NotFound("content.revision_not_found", "No such revision.")
	}
	return int32(n), nil
}

func (h *Handler) revision(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	rev, err := revParam(r)
	if err != nil {
		return err
	}
	res, err := h.svc.RevisionDetail(r.Context(), principal(r), id, rev)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) restore(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	rev, err := revParam(r)
	if err != nil {
		return err
	}
	match, err := ifMatch(r)
	if err != nil {
		return err
	}
	d, err := h.svc.Restore(r.Context(), principal(r), id, rev, match)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusOK, d)
	return nil
}

func (h *Handler) reviews(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	res, err := h.svc.Reviews(r.Context(), principal(r), id)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
	return nil
}

// step adapts a body-less workflow action (submit, withdraw, publish, archive).
func (h *Handler) step(action func(*Service, context.Context, auth.Principal, int64) (Draft, error)) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := httpx.PathID(r, "id")
		if err != nil {
			return err
		}
		d, err := action(h.svc, r.Context(), principal(r), id)
		if err != nil {
			return err
		}
		writeDraft(w, http.StatusOK, d)
		return nil
	}
}

func (h *Handler) review(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in ReviewInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := h.svc.Review(r.Context(), principal(r), id, in)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusOK, d)
	return nil
}

func (h *Handler) schedule(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in ScheduleInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := h.svc.Schedule(r.Context(), principal(r), id, in)
	if err != nil {
		return err
	}
	writeDraft(w, http.StatusOK, d)
	return nil
}
