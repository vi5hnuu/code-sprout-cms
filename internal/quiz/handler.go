package quiz

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/quiz/quizdb"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes: Studio editing, the public page, and the signed-in reader's taking of the quiz.
// `attempts` throttles grading (each submit writes an attempt).
func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, attempts httpx.Middleware) {
	me := func(pattern string, fn httpx.HandlerFunc) { mux.Handle(pattern, authed(fn)) }
	me("POST /v1/studio/quizzes", h.create)
	me("GET /v1/studio/quizzes/{id}", h.studio)
	me("PUT /v1/studio/quizzes/{id}/settings", h.settings)
	me("PUT /v1/studio/quizzes/{id}/questions", h.questions)

	mux.Handle("GET /v1/public/quizzes/{slug}", httpx.HandlerFunc(h.page))

	me("GET /v1/me/quizzes/{id}/questions", h.sheet)
	mux.Handle("POST /v1/me/quizzes/{id}/attempts", authed(attempts(httpx.HandlerFunc(h.submit))))
	me("GET /v1/me/quizzes/{id}/attempts", h.attempts)
	me("GET /v1/me/quiz-state", h.states)
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "private, no-store") }

func writeStudio(w http.ResponseWriter, v StudioView, err error) error {
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, v)
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in content.DraftInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	d, err := h.svc.Create(r.Context(), principal(r), in)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(int(d.DraftRevNo))))
	httpx.JSON(w, http.StatusCreated, d)
	return nil
}

func (h *Handler) studio(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	v, err := h.svc.Studio(r.Context(), principal(r), id)
	return writeStudio(w, v, err)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in Settings
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.UpdateSettings(r.Context(), principal(r), id, in)
	return writeStudio(w, v, err)
}

func (h *Handler) questions(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Questions []QuestionInput `json:"questions"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.ReplaceQuestions(r.Context(), principal(r), id, in.Questions)
	return writeStudio(w, v, err)
}

func locale(r *http.Request) string {
	if l := r.URL.Query().Get("locale"); l != "" {
		return l
	}
	return "en"
}

// page: the public quiz page, cached like a problem statement (no questions in it).
func (h *Handler) page(w http.ResponseWriter, r *http.Request) error {
	loc := locale(r)
	p, redirect, err := h.svc.PublicPage(r.Context(), loc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	if redirect != nil {
		w.Header().Set("Location", "/v1/public/quizzes/"+url.PathEscape(redirect.Slug)+"?locale="+url.QueryEscape(loc))
		httpx.Public(w, 5*time.Minute, 24*time.Hour)
		httpx.JSON(w, http.StatusMovedPermanently, map[string]any{"redirect": redirect})
		return nil
	}
	httpx.Public(w, 5*time.Minute, 24*time.Hour, "content:"+strconv.FormatInt(p.ID, 10))
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (h *Handler) sheet(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	sheet, err := h.svc.Questions(r.Context(), id)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, sheet)
	return nil
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Version int32    `json:"version"`
		Answers []Answer `json:"answers"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	g, err := h.svc.Submit(r.Context(), principal(r), id, in.Version, in.Answers)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, g)
	return nil
}

func (h *Handler) attempts(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	req, err := page.Parse[AttemptCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Attempts(r.Context(), principal(r), id, req)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// states: GET /v1/me/quiz-state?ids=1,2,3
func (h *Handler) states(w http.ResponseWriter, r *http.Request) error {
	ids, err := httpx.QueryIDs(r, "ids", maxStateIDs)
	if err != nil {
		return err
	}
	res, err := h.svc.States(r.Context(), principal(r), ids)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
	return nil
}

// EraseUser deletes a reader's quiz attempts and scores (account erase), inside the caller's
// transaction. Quizzes they authored are content and follow content's erase rules.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	q := quizdb.New(tx)
	if err := q.DeleteUserAttempts(ctx, userID); err != nil {
		return err
	}
	return q.DeleteUserQuizzes(ctx, userID)
}
