package content

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

// Pages are cached at the CDN for 5 minutes and served stale for a day while refreshing; a
// publish purges content:<id> so edits show up within seconds regardless.
const (
	pageMaxAge = 5 * time.Minute
	pageStale  = 24 * time.Hour
	listMaxAge = time.Minute
	listStale  = time.Hour
)

type PublicHandler struct{ r *Reader }

func NewPublicHandler(r *Reader) *PublicHandler { return &PublicHandler{r: r} }

func (h *PublicHandler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/public/content", httpx.HandlerFunc(h.list))
	mux.Handle("GET /v1/public/content/{type}/{slug}", httpx.HandlerFunc(h.page))
}

// parseType accepts the lower-case URL form (/content/article/...).
func parseType(s string) (Type, bool) {
	t := Type(strings.ToUpper(s))
	switch t {
	case TypeArticle, TypeBlog, TypeProblem, TypeTopicPage, TypeQuiz, TypeVideo:
		return t, true
	}
	return "", false
}

func locale(r *http.Request) string {
	if l := r.URL.Query().Get("locale"); l != "" {
		return l
	}
	return "en"
}

func (h *PublicHandler) page(w http.ResponseWriter, r *http.Request) error {
	t, ok := parseType(r.PathValue("type"))
	if !ok {
		return errNotFound
	}
	loc := locale(r)
	p, redirect, err := h.r.Page(r.Context(), t, loc, r.PathValue("slug"))
	if err != nil {
		return err
	}
	if redirect != nil {
		// Permanent: old slugs never come back to this item. The web app turns this into its
		// own 301 so search engines transfer the page's ranking.
		target := "/v1/public/content/" + strings.ToLower(string(t)) + "/" + url.PathEscape(redirect.Slug) + "?locale=" + url.QueryEscape(loc)
		w.Header().Set("Location", target)
		httpx.Public(w, pageMaxAge, pageStale)
		httpx.JSON(w, http.StatusMovedPermanently, map[string]any{"redirect": redirect})
		return nil
	}
	httpx.Public(w, pageMaxAge, pageStale, "content:"+strconv.FormatInt(p.ID, 10))
	httpx.JSON(w, http.StatusOK, p)
	return nil
}

func (h *PublicHandler) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[PublishedCursor](r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := ListFilter{Locale: locale(r), Category: q.Get("category"), Tag: q.Get("tag"), Author: q.Get("author")}
	if ts := q.Get("type"); ts != "" {
		t, ok := parseType(ts)
		if !ok {
			return apperr.ValidationFailed(map[string]string{"type": "Unknown content type."})
		}
		f.Type = &t
	}
	res, err := h.r.List(r.Context(), f, req)
	if err != nil {
		return err
	}
	httpx.Public(w, listMaxAge, listStale, "listings")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}
