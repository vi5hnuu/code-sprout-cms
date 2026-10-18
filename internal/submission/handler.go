package submission

import (
	"net/http"
	"strconv"
	"strings"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/ratelimit"
)

type Handler struct {
	svc      *Service
	clientIP ratelimit.ClientIP
}

func NewHandler(svc *Service, clientIP ratelimit.ClientIP) *Handler {
	return &Handler{svc: svc, clientIP: clientIP}
}

// Limits: anonymous snippet runs get the tightest budget (they cost judge time and need no
// account); signed-in runs and submissions have their own.
type Limits struct {
	Runs        httpx.Middleware
	Submissions httpx.Middleware
}

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, l Limits) {
	mux.Handle("POST /v1/run", l.Runs(httpx.HandlerFunc(h.runSnippet)))
	mux.Handle("GET /v1/run/{runId}", httpx.HandlerFunc(h.runResult))
	mux.Handle("POST /v1/problems/{id}/run", authed(l.Runs(httpx.HandlerFunc(h.runProblem))))

	me := func(pattern string, fn httpx.HandlerFunc) { mux.Handle(pattern, authed(fn)) }
	mux.Handle("POST /v1/me/submissions", authed(l.Submissions(httpx.HandlerFunc(h.submit))))
	me("GET /v1/me/submissions", h.list)
	me("GET /v1/me/submissions/{id}", h.get)
	me("GET /v1/me/problem-state", h.states)
	me("GET /v1/me/solved", h.solved)
	me("GET /v1/me/stats", h.stats)
	me("GET /v1/me/reviews", h.reviews)
	me("DELETE /v1/me/reviews/{problemId}", h.dismissReview)
}

// reviews: ?when=due (default) or upcoming, keyset paged.
func (h *Handler) reviews(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[ReviewCursor](r)
	if err != nil {
		return err
	}
	when := r.URL.Query().Get("when")
	if when != "" && when != "due" && when != "upcoming" {
		return apperr.ValidationFailed(map[string]string{"when": "when must be due or upcoming."})
	}
	res, err := h.svc.Reviews(r.Context(), principal(r), when != "upcoming", req)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) dismissReview(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("problemId"), 10, 64)
	if err != nil || id <= 0 {
		return apperr.NotFound("review.not_found", "That problem is not scheduled for review.")
	}
	if err := h.svc.DismissReview(r.Context(), principal(r), id); err != nil {
		return err
	}
	noStore(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// owner is who a run belongs to: the signed-in user, else the client address.
func (h *Handler) owner(r *http.Request) string {
	if p, ok := auth.FromContext(r.Context()); ok {
		return "u" + strconv.FormatInt(p.UserID, 10)
	}
	return "ip" + h.clientIP(r)
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "private, no-store") }

func (h *Handler) runSnippet(w http.ResponseWriter, r *http.Request) error {
	var in SnippetInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	t, err := h.svc.RunSnippet(r.Context(), h.owner(r), in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, t)
	return nil
}

func (h *Handler) runProblem(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in ProblemRunInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	t, err := h.svc.RunProblem(r.Context(), h.owner(r), id, in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, t)
	return nil
}

func (h *Handler) runResult(w http.ResponseWriter, r *http.Request) error {
	res, err := h.svc.RunResult(r.Context(), h.owner(r), r.PathValue("runId"))
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) error {
	var in SubmitInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	s, err := h.svc.Submit(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusAccepted, s)
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	s, err := h.svc.Get(r.Context(), principal(r), id)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, s)
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[SubmissionCursor](r)
	if err != nil {
		return err
	}
	var problemID *int64
	if v := r.URL.Query().Get("problemId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return apperr.ValidationFailed(map[string]string{"problemId": "problemId must be a positive integer."})
		}
		problemID = &id
	}
	res, err := h.svc.List(r.Context(), principal(r), problemID, req)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) states(w http.ResponseWriter, r *http.Request) error {
	var ids []int64
	if raw := r.URL.Query().Get("ids"); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err != nil || id <= 0 {
				return apperr.ValidationFailed(map[string]string{"ids": "ids must be comma-separated positive integers."})
			}
			ids = append(ids, id)
		}
	}
	st, err := h.svc.States(r.Context(), principal(r), ids)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": st})
	return nil
}

func (h *Handler) solved(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[SolvedCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Solved(r.Context(), principal(r), req)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) stats(w http.ResponseWriter, r *http.Request) error {
	st, err := h.svc.Stats(r.Context(), principal(r))
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, st)
	return nil
}
