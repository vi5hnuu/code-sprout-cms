// Package library serves a reader's saved and liked items. It composes two domains:
// engagement knows *what* the reader saved and when; content knows how to show each item.
// Keeping the composition here means neither domain has to depend on the other.
package library

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

type Entries interface {
	Bookmarks(ctx context.Context, p auth.Principal, listID *int64, req page.Request[engagement.EntryCursor]) (page.Page[engagement.Entry], error)
	Likes(ctx context.Context, p auth.Principal, req page.Request[engagement.EntryCursor]) (page.Page[engagement.Entry], error)
}

type Items interface {
	ItemsByID(ctx context.Context, ids []int64) ([]content.ListItem, error)
}

type Handler struct {
	entries Entries
	items   Items
}

func NewHandler(entries Entries, items Items) *Handler {
	return &Handler{entries: entries, items: items}
}

func (h *Handler) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/me/bookmarks", authed(httpx.HandlerFunc(h.bookmarks)))
	mux.Handle("GET /v1/me/likes", authed(httpx.HandlerFunc(h.likes)))
}

// Saved pairs an entry with the item it points to. Items that are no longer live are left
// out of the page; the cursor still advances past them.
type Saved struct {
	SavedAt time.Time        `json:"savedAt"`
	ListID  *int64           `json:"listId,omitempty"`
	Item    content.ListItem `json:"item"`
}

func (h *Handler) bookmarks(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[engagement.EntryCursor](r)
	if err != nil {
		return err
	}
	var listID *int64
	if v := r.URL.Query().Get("listId"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			return apperr.ValidationFailed(map[string]string{"listId": "listId must be a positive integer."})
		}
		listID = &id
	}
	p, _ := auth.FromContext(r.Context())
	entries, err := h.entries.Bookmarks(r.Context(), p, listID, req)
	if err != nil {
		return err
	}
	return h.respond(w, r, entries)
}

func (h *Handler) likes(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[engagement.EntryCursor](r)
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	entries, err := h.entries.Likes(r.Context(), p, req)
	if err != nil {
		return err
	}
	return h.respond(w, r, entries)
}

func (h *Handler) respond(w http.ResponseWriter, r *http.Request, entries page.Page[engagement.Entry]) error {
	ids := make([]int64, len(entries.Items))
	for i, e := range entries.Items {
		ids[i] = e.ContentID
	}
	items, err := h.items.ItemsByID(r.Context(), ids)
	if err != nil {
		return err
	}
	byID := make(map[int64]content.ListItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := make([]Saved, 0, len(entries.Items))
	for _, e := range entries.Items {
		if it, ok := byID[e.ContentID]; ok {
			out = append(out, Saved{SavedAt: e.SavedAt, ListID: e.ListID, Item: it})
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, page.Page[Saved]{Items: out, NextCursor: entries.NextCursor})
	return nil
}
