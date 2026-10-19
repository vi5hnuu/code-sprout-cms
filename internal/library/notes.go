package library

import (
	"context"
	"net/http"
	"strings"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/library/librarydb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

// Notes are a reader's private notes on any item (an approach to a problem, takeaways from an
// article). Only the owner can ever read them.

const maxNoteBytes = 64 << 10

type Note struct {
	ContentID int64     `json:"contentId"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Notes struct {
	db    *db.DB
	items Items
}

func NewNotes(d *db.DB, items Items) *Notes { return &Notes{db: d, items: items} }

func (n *Notes) Get(ctx context.Context, p auth.Principal, contentID int64) (Note, error) {
	r, err := librarydb.New(n.db.Primary).GetNote(ctx, librarydb.GetNoteParams{UserID: p.UserID, ContentID: contentID})
	if db.IsNoRows(err) {
		return Note{}, apperr.NotFound("note.not_found", "No note yet.")
	}
	if err != nil {
		return Note{}, err
	}
	return Note{ContentID: r.ContentID, Body: r.BodyMd, UpdatedAt: r.UpdatedAt}, nil
}

// Put saves the note; an empty body deletes it.
func (n *Notes) Put(ctx context.Context, p auth.Principal, contentID int64, body string) (*Note, error) {
	if len(body) > maxNoteBytes {
		return nil, apperr.ValidationFailed(map[string]string{"body": "Notes are at most 64 KiB."})
	}
	q := librarydb.New(n.db.Primary)
	if strings.TrimSpace(body) == "" {
		return nil, q.DeleteNote(ctx, librarydb.DeleteNoteParams{UserID: p.UserID, ContentID: contentID})
	}
	// Notes attach to live items only: otherwise any id would accept a note (orphan rows, and
	// storage anyone could fill).
	items, err := n.items.ItemsByID(ctx, []int64{contentID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, apperr.NotFound("content.not_found", "No such page.")
	}
	r, err := q.UpsertNote(ctx, librarydb.UpsertNoteParams{UserID: p.UserID, ContentID: contentID, BodyMd: body})
	if err != nil {
		return nil, err
	}
	return &Note{ContentID: r.ContentID, Body: r.BodyMd, UpdatedAt: r.UpdatedAt}, nil
}

type NoteCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

func (n *Notes) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/me/notes", authed(httpx.HandlerFunc(n.list)))
	mux.Handle("GET /v1/me/notes/{contentId}", authed(httpx.HandlerFunc(n.get)))
	mux.Handle("PUT /v1/me/notes/{contentId}", authed(httpx.HandlerFunc(n.put)))
}

func (n *Notes) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	note, err := n.Get(r.Context(), p, id)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, note)
	return nil
}

func (n *Notes) put(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	note, err := n.Put(r.Context(), p, id, in.Body)
	if err != nil {
		return err
	}
	if note == nil {
		httpx.NoContent(w)
		return nil
	}
	httpx.JSON(w, http.StatusOK, note)
	return nil
}

// list shows the items the caller has notes on, most recently edited first.
func (n *Notes) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[NoteCursor](r)
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	params := librarydb.ListNotesParams{UserID: p.UserID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterUpdated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := librarydb.New(n.db.Primary).ListNotes(r.Context(), params)
	if err != nil {
		return err
	}
	built := page.Build(rows, req.Limit, func(r librarydb.ListNotesRow) NoteCursor { return NoteCursor{At: r.UpdatedAt, ID: r.ContentID} })
	ids := make([]int64, len(built.Items))
	updated := make(map[int64]time.Time, len(built.Items))
	for i, row := range built.Items {
		ids[i] = row.ContentID
		updated[row.ContentID] = row.UpdatedAt
	}
	items, err := n.items.ItemsByID(r.Context(), ids)
	if err != nil {
		return err
	}
	out := make([]Saved, len(items))
	for i, it := range items {
		out[i] = Saved{SavedAt: updated[it.ID], Item: it}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, page.Page[Saved]{Items: out, NextCursor: built.NextCursor})
	return nil
}
