package search

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
)

const (
	maxLimit  = 50
	maxOffset = 1000 // search results deeper than this are not useful; refine the query instead
	maxQuery  = 200
)

type Handler struct {
	index   Index
	indexer *Indexer
	policy  *access.Policy
}

func NewHandler(index Index, indexer *Indexer, policy *access.Policy) *Handler {
	return &Handler{index: index, indexer: indexer, policy: policy}
}

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, limit httpx.Middleware) {
	mux.Handle("GET /v1/public/search", limit(httpx.HandlerFunc(h.search)))
	// Reindex is destructive and expensive; admin-only.
	mux.Handle("POST /v1/admin/search/reindex", auth.RequireAdmin(httpx.HandlerFunc(h.reindex)))
}

// Search is the one place offset paging is used: the engine ranks by relevance (no stable
// keyset), and depth is capped at maxOffset.
func (h *Handler) search(w http.ResponseWriter, r *http.Request) error {
	qs := r.URL.Query()
	text := strings.TrimSpace(qs.Get("q"))
	f := map[string]string{}
	if n := len([]rune(text)); n < 2 || n > maxQuery {
		f["q"] = "Search for 2 to 200 characters."
	}
	limit, offset := 20, 0
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit {
			f["limit"] = "limit must be 1-50."
		}
		limit = n
	}
	if v := qs.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxOffset {
			f["offset"] = "offset must be 0-1000."
		}
		offset = n
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	locale := qs.Get("locale")
	if locale == "" {
		locale = "en"
	}
	res, err := h.index.Search(r.Context(), Query{
		Text: text, Type: strings.ToUpper(qs.Get("type")), Locale: locale, Difficulty: strings.ToUpper(qs.Get("difficulty")),
		Limit: limit, Offset: offset,
	})
	if errors.Is(err, ErrUnavailable) {
		return &apperr.Error{Kind: apperr.KindUnavailable, Code: "search.unavailable", Detail: "Search is temporarily unavailable.", Err: err}
	}
	if err != nil {
		return err
	}
	httpx.Public(w, time.Minute, 10*time.Minute, "search")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) reindex(w http.ResponseWriter, r *http.Request) error {
	p, _ := auth.FromContext(r.Context())
	if err := h.policy.Require(r.Context(), p, access.RoleAdmin, nil); err != nil {
		return err
	}
	if err := h.indexer.RequestReindex(r.Context()); err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}
