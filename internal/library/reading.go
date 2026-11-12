package library

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/library/librarydb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/httpx"
	"code-sprout-cms/internal/platform/page"
)

// Reading progress powers "Continue reading": where each signed-in reader last was on each
// page, and which pages they finished. The page saves the position as the reader moves
// between sections (throttled client-side, plus once when the tab is hidden), so one active
// reader costs about one small upsert every fifteen seconds.

const (
	// finishedAtPercent marks a page read: readers rarely scroll through footers and comments.
	finishedAtPercent = 90
	maxHeadingIDLen   = 200
	maxReadingIDs     = 100
	// readingRetention bounds the table: positions untouched for a year are pruned.
	readingRetention = 365 * 24 * time.Hour
	pruneBatch       = 5000
)

// Position is where a reader is on one page.
type Position struct {
	ContentID  int64      `json:"contentId"`
	HeadingID  *string    `json:"headingId"` // section anchor to resume at; nil = the top
	Percent    int16      `json:"percent"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

func positionOf(r librarydb.ReadingProgress) Position {
	return Position{ContentID: r.ContentID, HeadingID: r.HeadingID, Percent: r.Percent, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt}
}

// Reading is one entry of the Continue / Finished lists: the position and the page it is on.
type Reading struct {
	Position
	Item content.ListItem `json:"item"`
}

type ReadingCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

type ReadingState string

const (
	ReadingInProgress ReadingState = "in-progress"
	ReadingFinished   ReadingState = "finished"
)

type Progress struct {
	db    *db.DB
	items Items
}

func NewProgress(d *db.DB, items Items) *Progress { return &Progress{db: d, items: items} }

// Save records the reader's position on a live page and returns it.
func (s *Progress) Save(ctx context.Context, p auth.Principal, contentID int64, headingID *string, percent int) (Position, error) {
	if percent < 0 || percent > 100 {
		return Position{}, apperr.ValidationFailed(map[string]string{"percent": "percent must be between 0 and 100."})
	}
	if headingID != nil {
		h := strings.TrimSpace(*headingID)
		switch {
		case h == "":
			headingID = nil
		case len(h) > maxHeadingIDLen || strings.ContainsFunc(h, unicode.IsSpace):
			// Heading anchors are slugs (internal/markdown/slug.go): no whitespace, short.
			return Position{}, apperr.ValidationFailed(map[string]string{"headingId": "headingId must be a heading anchor."})
		default:
			headingID = &h
		}
	}
	// Progress attaches to live pages only, so arbitrary ids cannot fill the table.
	live, err := s.items.LiveIDs(ctx, []int64{contentID})
	if err != nil {
		return Position{}, err
	}
	if len(live) == 0 {
		return Position{}, apperr.NotFound("content.not_found", "No such page.")
	}
	row, err := librarydb.New(s.db.Primary).UpsertReading(ctx, librarydb.UpsertReadingParams{
		UserID: p.UserID, ContentID: contentID, HeadingID: headingID, Percent: int16(percent), FinishedAtPercent: finishedAtPercent,
	})
	if err != nil {
		return Position{}, err
	}
	return positionOf(row), nil
}

// Forget removes a page from the reader's lists.
func (s *Progress) Forget(ctx context.Context, p auth.Principal, contentID int64) error {
	return librarydb.New(s.db.Primary).DeleteReading(ctx, librarydb.DeleteReadingParams{UserID: p.UserID, ContentID: contentID})
}

// List returns the reader's pages in progress (most recently read first) or finished (most
// recently finished first), with each page's card. Pages no longer live are left out; the
// cursor still advances past them.
func (s *Progress) List(ctx context.Context, p auth.Principal, state ReadingState, req page.Request[ReadingCursor]) (page.Page[Reading], error) {
	q := librarydb.New(s.db.Primary)
	var (
		rows []librarydb.ReadingProgress
		err  error
	)
	var afterAt *time.Time
	var afterID *int64
	if req.Key != nil {
		afterAt, afterID = &req.Key.At, &req.Key.ID
	}
	limit := int32(req.Limit + 1)
	switch state {
	case ReadingInProgress:
		rows, err = q.ListReadingInProgress(ctx, librarydb.ListReadingInProgressParams{UserID: p.UserID, AfterAt: afterAt, AfterID: afterID, RowLimit: limit})
	case ReadingFinished:
		rows, err = q.ListReadingFinished(ctx, librarydb.ListReadingFinishedParams{UserID: p.UserID, AfterAt: afterAt, AfterID: afterID, RowLimit: limit})
	default:
		return page.Page[Reading]{}, apperr.ValidationFailed(map[string]string{"state": "state must be in-progress or finished."})
	}
	if err != nil {
		return page.Page[Reading]{}, err
	}
	built := page.Build(rows, req.Limit, func(r librarydb.ReadingProgress) ReadingCursor {
		if state == ReadingFinished {
			return ReadingCursor{At: *r.FinishedAt, ID: r.ContentID}
		}
		return ReadingCursor{At: r.UpdatedAt, ID: r.ContentID}
	})
	ids := make([]int64, len(built.Items))
	for i, r := range built.Items {
		ids[i] = r.ContentID
	}
	items, err := s.items.ItemsByID(ctx, ids)
	if err != nil {
		return page.Page[Reading]{}, err
	}
	byID := make(map[int64]content.ListItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := make([]Reading, 0, len(built.Items))
	for _, r := range built.Items {
		if it, ok := byID[r.ContentID]; ok {
			out = append(out, Reading{Position: positionOf(r), Item: it})
		}
	}
	return page.Page[Reading]{Items: out, NextCursor: built.NextCursor}, nil
}

// States returns the reader's positions on up to 100 pages (a series or study plan showing
// which of its articles are read). Pages never opened are absent.
func (s *Progress) States(ctx context.Context, p auth.Principal, ids []int64) ([]Position, error) {
	if len(ids) > maxReadingIDs {
		return nil, apperr.ValidationFailed(map[string]string{"ids": fmt.Sprintf("At most %d ids.", maxReadingIDs)})
	}
	out := []Position{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := librarydb.New(s.db.Primary).ReadingStates(ctx, librarydb.ReadingStatesParams{UserID: p.UserID, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, positionOf(r))
	}
	return out, nil
}

// Prune deletes positions untouched for a year, in batches (worker upkeep).
func (s *Progress) Prune(ctx context.Context) error {
	before := time.Now().Add(-readingRetention)
	var total int64
	for {
		n, err := librarydb.New(s.db.Primary).PruneReading(ctx, librarydb.PruneReadingParams{Before: before, Batch: pruneBatch})
		if err != nil {
			return err
		}
		total += n
		if n < pruneBatch {
			break
		}
	}
	if total > 0 {
		slog.Info("reading progress pruned", "rows", total)
	}
	return nil
}

func (s *Progress) Routes(mux *http.ServeMux, authed func(http.Handler) http.Handler, limit httpx.Middleware) {
	mux.Handle("PUT /v1/me/reading/{contentId}", authed(limit(httpx.HandlerFunc(s.save))))
	mux.Handle("DELETE /v1/me/reading/{contentId}", authed(httpx.HandlerFunc(s.forget)))
	mux.Handle("GET /v1/me/reading", authed(httpx.HandlerFunc(s.list)))
	mux.Handle("GET /v1/me/reading-state", authed(httpx.HandlerFunc(s.states)))
}

func (s *Progress) save(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	var in struct {
		HeadingID *string `json:"headingId"`
		Percent   int     `json:"percent"`
	}
	if err := httpx.Decode(w, r, &in); err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	pos, err := s.Save(r.Context(), p, id, in.HeadingID, in.Percent)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, pos)
	return nil
}

func (s *Progress) forget(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathID(r, "contentId")
	if err != nil {
		return err
	}
	p, _ := auth.FromContext(r.Context())
	if err := s.Forget(r.Context(), p, id); err != nil {
		return err
	}
	httpx.NoContent(w)
	return nil
}

func (s *Progress) list(w http.ResponseWriter, r *http.Request) error {
	req, err := page.Parse[ReadingCursor](r)
	if err != nil {
		return err
	}
	state := ReadingState(r.URL.Query().Get("state"))
	if state == "" {
		state = ReadingInProgress
	}
	p, _ := auth.FromContext(r.Context())
	res, err := s.List(r.Context(), p, state, req)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, res)
	return nil
}

// states: GET /v1/me/reading-state?ids=1,2,3
func (s *Progress) states(w http.ResponseWriter, r *http.Request) error {
	var ids []int64
	if raw := r.URL.Query().Get("ids"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id <= 0 {
				return apperr.ValidationFailed(map[string]string{"ids": "ids must be comma-separated positive integers."})
			}
			ids = append(ids, id)
			if len(ids) > maxReadingIDs {
				return apperr.ValidationFailed(map[string]string{"ids": fmt.Sprintf("At most %d ids.", maxReadingIDs)})
			}
		}
	}
	p, _ := auth.FromContext(r.Context())
	res, err := s.States(r.Context(), p, ids)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"items": res})
	return nil
}
