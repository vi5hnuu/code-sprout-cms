package problem

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	studio := func(pattern string, fn httpx.HandlerFunc) { mux.Handle(pattern, authed(fn)) }
	studio("POST /v1/studio/problems", h.create)
	studio("GET /v1/studio/problems/{id}", h.studio)
	studio("PUT /v1/studio/problems/{id}/settings", h.settings)
	studio("PUT /v1/studio/problems/{id}/samples", h.samples)
	studio("PUT /v1/studio/problems/{id}/templates/{language}", h.putTemplate)
	studio("DELETE /v1/studio/problems/{id}/templates/{language}", h.deleteTemplate)
	studio("PUT /v1/studio/problems/{id}/hints", h.putHints)
	studio("POST /v1/studio/problems/{id}/test-sets", h.addTestSet)
	studio("POST /v1/studio/problems/{id}/test-sets/{version}/activate", h.activateTestSet)
	// Scheduling the problem-of-the-day is an admin surface, not a studio-author one.
	mux.Handle("PUT /v1/admin/problem-of-the-day/{date}", auth.RequireAdmin(httpx.HandlerFunc(h.scheduleOfDay)))

	mux.Handle("GET /v1/public/problems", httpx.HandlerFunc(h.list))
	mux.Handle("GET /v1/public/random-problem", httpx.HandlerFunc(h.random))
	mux.Handle("GET /v1/public/problems/{slug}", httpx.HandlerFunc(h.page))
	mux.Handle("GET /v1/public/problems/{id}/hints", httpx.HandlerFunc(h.hints))
	mux.Handle("GET /v1/public/problem-of-the-day", httpx.HandlerFunc(h.today))
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func writeStudio(w http.ResponseWriter, v StudioView, err error) error {
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, v)
	return nil
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

func (h *Handler) samples(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Samples []Sample `json:"samples"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.ReplaceSamples(r.Context(), principal(r), id, in.Samples)
	return writeStudio(w, v, err)
}

func (h *Handler) putTemplate(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		StarterCode string  `json:"starterCode"`
		DriverCode  *string `json:"driverCode"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.PutTemplate(r.Context(), principal(r), id, Template{Language: r.PathValue("language"), StarterCode: in.StarterCode, DriverCode: in.DriverCode})
	return writeStudio(w, v, err)
}

func (h *Handler) deleteTemplate(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	v, err := h.svc.DeleteTemplate(r.Context(), principal(r), id, r.PathValue("language"))
	return writeStudio(w, v, err)
}

func (h *Handler) putHints(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Hints []string `json:"hints"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.ReplaceHints(r.Context(), principal(r), id, in.Hints)
	return writeStudio(w, v, err)
}

func (h *Handler) addTestSet(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		FileID int64 `json:"fileId"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	v, err := h.svc.AddTestSet(r.Context(), principal(r), id, in.FileID)
	return writeStudio(w, v, err)
}

func (h *Handler) activateTestSet(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 32)
	if err != nil || version <= 0 {
		return apperr.NotFound("problem.test_set_not_found", "No such test set version.")
	}
	v, err := h.svc.ActivateTestSet(r.Context(), principal(r), id, int32(version))
	return writeStudio(w, v, err)
}

func (h *Handler) scheduleOfDay(w http.ResponseWriter, r *http.Request) error {
	day, err := time.Parse(time.DateOnly, r.PathValue("date"))
	if err != nil {
		return apperr.ValidationFailed(map[string]string{"date": "Use YYYY-MM-DD."})
	}
	var in struct {
		ProblemID int64 `json:"problemId"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.ScheduleOfDay(r.Context(), principal(r), day, in.ProblemID); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func locale(r *http.Request) string {
	if l := r.URL.Query().Get("locale"); l != "" {
		return l
	}
	return "en"
}

func (h *Handler) page(w http.ResponseWriter, r *http.Request) error {
	loc := locale(r)
	p, redirect, err := h.svc.Page(r.Context(), loc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	if redirect != nil {
		w.Header().Set("Location", "/v1/public/problems/"+url.PathEscape(redirect.Slug)+"?locale="+url.QueryEscape(loc))
		httpx.Public(w, 5*time.Minute, 24*time.Hour)
		httpx.JSON(w, http.StatusMovedPermanently, map[string]any{"redirect": redirect})
		return nil
	}
	httpx.Public(w, 5*time.Minute, 24*time.Hour, "content:"+strconv.FormatInt(p.ID, 10))
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[content.PublishedCursor](r)
	if err != nil {
		return err
	}
	f := ListFilter{Tag: r.URL.Query().Get("tag")}
	if d := r.URL.Query().Get("difficulty"); d != "" {
		diff := Difficulty(d)
		f.Difficulty = &diff
	}
	res, err := h.svc.List(r.Context(), f, req)
	if err != nil {
		return err
	}
	httpx.Public(w, time.Minute, time.Hour, "listings")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// random: one listed problem, ?difficulty= and ?tag=kind:slug optional. Never cached: every
// call should pick again.
func (h *Handler) random(w http.ResponseWriter, r *http.Request) error {
	f := RandomFilter{Tag: r.URL.Query().Get("tag")}
	if d := r.URL.Query().Get("difficulty"); d != "" {
		diff := Difficulty(d)
		f.Difficulty = &diff
	}
	item, err := h.svc.Random(r.Context(), f)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, item)
	return nil
}

func (h *Handler) hints(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	hints, err := h.svc.Hints(r.Context(), id)
	if err != nil {
		return err
	}
	httpx.Public(w, 10*time.Minute, 24*time.Hour, "content:"+strconv.FormatInt(id, 10))
	httpx.JSON(w, http.StatusOK, map[string]any{"items": hints})
	return nil
}

func (h *Handler) today(w http.ResponseWriter, r *http.Request) error {
	item, err := h.svc.Today(r.Context())
	if err != nil {
		return err
	}
	httpx.Public(w, 5*time.Minute, time.Hour, "problem-of-the-day")
	httpx.JSON(w, http.StatusOK, item)
	return nil
}
