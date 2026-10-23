package engagement

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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

// Limits are per-purpose throttles: reacting, commenting and anonymous counting events each
// get their own budget so one cannot starve the others.
type Limits struct {
	Actions  httpx.Middleware
	Comments httpx.Middleware
	Events   httpx.Middleware
}

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, l Limits) {
	me := func(pattern string, limit httpx.Middleware, fn httpx.HandlerFunc) {
		mux.Handle(pattern, authed(limit(fn)))
	}
	me("PUT /v1/me/reactions/{contentId}", l.Actions, h.like)
	me("DELETE /v1/me/reactions/{contentId}", l.Actions, h.unlike)
	me("PUT /v1/me/bookmarks/{contentId}", l.Actions, h.bookmark)
	me("DELETE /v1/me/bookmarks/{contentId}", l.Actions, h.unbookmark)
	me("GET /v1/me/bookmark-lists", l.Actions, h.lists)
	me("POST /v1/me/bookmark-lists", l.Actions, h.createList)
	me("DELETE /v1/me/bookmark-lists/{id}", l.Actions, h.deleteList)
	me("GET /v1/me/state", l.Actions, h.state)

	me("POST /v1/me/comments/{contentId}", l.Comments, h.comment)
	me("PUT /v1/me/comments/{contentId}/{commentId}", l.Comments, h.editComment)
	me("DELETE /v1/me/comments/{contentId}/{commentId}", l.Actions, h.deleteComment)
	me("POST /v1/me/comments/{contentId}/{commentId}/report", l.Comments, h.report)

	mux.Handle("GET /v1/public/threads/{contentId}", httpx.HandlerFunc(h.thread))
	mux.Handle("GET /v1/public/threads/{contentId}/{rootId}/replies", httpx.HandlerFunc(h.replies))
	mux.Handle("POST /v1/public/events/view", l.Events(httpx.HandlerFunc(h.view)))
	mux.Handle("POST /v1/public/events/share", l.Events(httpx.HandlerFunc(h.share)))

	// Moderation surface: admin-only, not every signed-in reader.
	mux.Handle("GET /v1/admin/comment-reports", auth.RequireAdmin(httpx.HandlerFunc(h.reports)))
	mux.Handle("POST /v1/admin/comments/{contentId}/{commentId}/moderate", auth.RequireAdmin(httpx.HandlerFunc(h.moderate)))
}

func principal(r *http.Request) auth.Principal {
	p, _ := auth.FromContext(r.Context())
	return p
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "private, no-store") }

func (h *Handler) like(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	if err := h.svc.Like(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) unlike(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	if err := h.svc.Unlike(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) bookmark(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	var in struct {
		ListID *int64 `json:"listId"`
	}
	if r.ContentLength != 0 {
		if err := httpx.Decode(w, r, &in); err != nil {
			return err
		}
	}
	if err := h.svc.Bookmark(r.Context(), principal(r), id, in.ListID); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) unbookmark(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	if err := h.svc.Unbookmark(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) lists(w http.ResponseWriter, r *http.Request) error {
	lists, err := h.svc.BookmarkLists(r.Context(), principal(r))
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": lists})
	return nil
}

func (h *Handler) createList(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	l, err := h.svc.CreateBookmarkList(r.Context(), principal(r), in.Name)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, l)
	return nil
}

func (h *Handler) deleteList(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.DeleteBookmarkList(r.Context(), principal(r), id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

// state: GET /v1/me/state?ids=1,2,3
func (h *Handler) state(w http.ResponseWriter, r *http.Request) error {
	ids, err := parseIDs(r.URL.Query().Get("ids"))
	if err != nil {
		return err
	}
	st, err := h.svc.State(r.Context(), principal(r), ids)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": st})
	return nil
}

func parseIDs(raw string) ([]int64, error) {
	if raw == "" {
		return []int64{}, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxStateIDs {
		return nil, apperr.ValidationFailed(map[string]string{"ids": "At most 100 ids."})
	}
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || id <= 0 {
			return nil, apperr.ValidationFailed(map[string]string{"ids": "ids must be comma-separated positive integers."})
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type commentInput struct {
	ParentID *int64 `json:"parentId"`
	Body     string `json:"body"`
}

func (h *Handler) comment(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	var in commentInput
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	c, err := h.svc.CreateComment(r.Context(), principal(r), id, in.ParentID, in.Body)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusCreated, c)
	return nil
}

func commentIDs(r *http.Request) (int64, int64, error) {
	contentID, err := httpx.PathID(r, "contentId")
	if err != nil {
		return 0, 0, err
	}
	commentID, err := httpx.PathID(r, "commentId")
	return contentID, commentID, err
}

func (h *Handler) editComment(w http.ResponseWriter, r *http.Request) error {
	contentID, commentID, err := commentIDs(r)
	if err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	c, err := h.svc.EditComment(r.Context(), principal(r), contentID, commentID, in.Body)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, c)
	return nil
}

func (h *Handler) deleteComment(w http.ResponseWriter, r *http.Request) error {
	contentID, commentID, err := commentIDs(r)
	if err != nil {
		return err
	}
	if err := h.svc.DeleteComment(r.Context(), principal(r), contentID, commentID); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) report(w http.ResponseWriter, r *http.Request) error {
	contentID, commentID, err := commentIDs(r)
	if err != nil {
		return err
	}
	var in struct {
		Reason string  `json:"reason"`
		Note   *string `json:"note"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.ReportComment(r.Context(), principal(r), contentID, commentID, in.Reason, in.Note); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

// Threads change often, so the CDN keeps them only briefly; comments.changed purges sooner.
func (h *Handler) thread(w http.ResponseWriter, r *http.Request) error {
	contentID, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	req, err := page.Parse[CommentCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Thread(r.Context(), contentID, req)
	if err != nil {
		return err
	}
	httpx.Public(w, 30*time.Second, 5*time.Minute, "comments:"+strconv.FormatInt(contentID, 10))
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) replies(w http.ResponseWriter, r *http.Request) error {
	contentID, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	rootID, err := httpx.PathID(r, "rootId")
	if err != nil {
		return err
	}
	req, err := page.Parse[CommentCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Replies(r.Context(), contentID, rootID, req)
	if err != nil {
		return err
	}
	httpx.Public(w, 30*time.Second, 5*time.Minute, "comments:"+strconv.FormatInt(contentID, 10))
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// viewer is the dedupe identity for counting: the user when signed in, else the client address.
func (h *Handler) viewer(r *http.Request) string {
	if p, ok := auth.FromContext(r.Context()); ok {
		return "u" + strconv.FormatInt(p.UserID, 10)
	}
	return "ip" + h.clientIP(r)
}

func (h *Handler) view(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ContentID int64 `json:"contentId"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.RecordView(r.Context(), in.ContentID, h.viewer(r)); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) share(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ContentID int64  `json:"contentId"`
		Channel   string `json:"channel"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.RecordShare(r.Context(), in.ContentID, h.viewer(r), in.Channel); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (h *Handler) reports(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[ReportCursor](r)
	if err != nil {
		return err
	}
	res, err := h.svc.Reports(r.Context(), principal(r), req)
	if err != nil {
		return err
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

func (h *Handler) moderate(w http.ResponseWriter, r *http.Request) error {
	contentID, commentID, err := commentIDs(r)
	if err != nil {
		return err
	}
	var in struct {
		Action ModerationAction `json:"action"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.Moderate(r.Context(), principal(r), contentID, commentID, in.Action); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}
