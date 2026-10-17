package engagement

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
)

const (
	maxBookmarksPerUser = 10_000
	maxBookmarkLists    = 200
	maxStateIDs         = 100
)

// Like and Unlike are idempotent: repeating either is a no-op, and the counter moves only
// when the row actually changes.
func (s *Service) Like(ctx context.Context, p auth.Principal, contentID int64) error {
	if err := s.requireLive(ctx, contentID); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		n, err := q.InsertReaction(ctx, engagementdb.InsertReactionParams{ContentID: contentID, UserID: p.UserID})
		if err != nil || n == 0 {
			return err
		}
		return applyDeltas(ctx, q, contentID, deltas{likes: 1})
	})
}

func (s *Service) Unlike(ctx context.Context, p auth.Principal, contentID int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		n, err := q.DeleteReaction(ctx, engagementdb.DeleteReactionParams{ContentID: contentID, UserID: p.UserID})
		if err != nil || n == 0 {
			return err
		}
		return applyDeltas(ctx, q, contentID, deltas{likes: -1})
	})
}

// Bookmark saves an item (optionally into a named list); bookmarking again moves it.
func (s *Service) Bookmark(ctx context.Context, p auth.Principal, contentID int64, listID *int64) error {
	if err := s.requireLive(ctx, contentID); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		if listID != nil {
			if _, err := q.GetBookmarkList(ctx, engagementdb.GetBookmarkListParams{ID: *listID, UserID: p.UserID}); db.IsNoRows(err) {
				return apperr.ValidationFailed(map[string]string{"listId": "No such list."})
			} else if err != nil {
				return err
			}
		}
		n, err := q.InsertBookmark(ctx, engagementdb.InsertBookmarkParams{UserID: p.UserID, ContentID: contentID, ListID: listID})
		if err != nil {
			return err
		}
		if n == 0 {
			return q.MoveBookmark(ctx, engagementdb.MoveBookmarkParams{UserID: p.UserID, ContentID: contentID, ListID: listID})
		}
		count, err := q.CountUserBookmarks(ctx, p.UserID)
		if err != nil {
			return err
		}
		if count > maxBookmarksPerUser {
			return apperr.Conflict("bookmark.limit", fmt.Sprintf("You can keep up to %d bookmarks.", maxBookmarksPerUser))
		}
		return applyDeltas(ctx, q, contentID, deltas{bookmarks: 1})
	})
}

func (s *Service) Unbookmark(ctx context.Context, p auth.Principal, contentID int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		n, err := q.DeleteBookmark(ctx, engagementdb.DeleteBookmarkParams{UserID: p.UserID, ContentID: contentID})
		if err != nil || n == 0 {
			return err
		}
		return applyDeltas(ctx, q, contentID, deltas{bookmarks: -1})
	})
}

type BookmarkList struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Service) CreateBookmarkList(ctx context.Context, p auth.Principal, name string) (BookmarkList, error) {
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n == 0 || n > 60 {
		return BookmarkList{}, apperr.ValidationFailed(map[string]string{"name": "Name is required and at most 60 characters."})
	}
	q := engagementdb.New(s.db.Primary)
	lists, err := q.ListBookmarkLists(ctx, p.UserID)
	if err != nil {
		return BookmarkList{}, err
	}
	if len(lists) >= maxBookmarkLists {
		return BookmarkList{}, apperr.Conflict("bookmark.list_limit", fmt.Sprintf("You can have up to %d lists.", maxBookmarkLists))
	}
	l, err := q.InsertBookmarkList(ctx, engagementdb.InsertBookmarkListParams{UserID: p.UserID, Name: name})
	if c, ok := db.ConstraintViolation(err); ok && c == "bookmark_list_user_id_name_key" {
		return BookmarkList{}, apperr.Conflict("bookmark.list_exists", "You already have a list with this name.")
	}
	if err != nil {
		return BookmarkList{}, err
	}
	return BookmarkList{ID: l.ID, Name: l.Name, CreatedAt: l.CreatedAt}, nil
}

func (s *Service) BookmarkLists(ctx context.Context, p auth.Principal) ([]BookmarkList, error) {
	rows, err := engagementdb.New(s.db.Primary).ListBookmarkLists(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]BookmarkList, len(rows))
	for i, l := range rows {
		out[i] = BookmarkList{ID: l.ID, Name: l.Name, CreatedAt: l.CreatedAt}
	}
	return out, nil
}

// DeleteBookmarkList removes the list; its bookmarks stay, moved back to the default list.
func (s *Service) DeleteBookmarkList(ctx context.Context, p auth.Principal, listID int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		if err := q.ClearBookmarkList(ctx, engagementdb.ClearBookmarkListParams{UserID: p.UserID, ListID: &listID}); err != nil {
			return err
		}
		n, err := q.DeleteBookmarkList(ctx, engagementdb.DeleteBookmarkListParams{ID: listID, UserID: p.UserID})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.NotFound("bookmark.list_not_found", "No such list.")
		}
		return nil
	})
}

// Entry is one saved or liked item in the caller's library.
type Entry struct {
	ContentID int64     `json:"contentId"`
	ListID    *int64    `json:"listId,omitempty"`
	SavedAt   time.Time `json:"savedAt"`
}

type EntryCursor struct {
	At time.Time `json:"t"`
	ID int64     `json:"i"`
}

func entryCursor(e Entry) EntryCursor { return EntryCursor{At: e.SavedAt, ID: e.ContentID} }

func (s *Service) Bookmarks(ctx context.Context, p auth.Principal, listID *int64, req page.Request[EntryCursor]) (page.Page[Entry], error) {
	params := engagementdb.ListUserBookmarksParams{UserID: p.UserID, ListID: listID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := engagementdb.New(s.db.Primary).ListUserBookmarks(ctx, params)
	if err != nil {
		return page.Page[Entry]{}, err
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = Entry{ContentID: r.ContentID, ListID: r.ListID, SavedAt: r.CreatedAt}
	}
	return page.Build(out, req.Limit, entryCursor), nil
}

func (s *Service) Likes(ctx context.Context, p auth.Principal, req page.Request[EntryCursor]) (page.Page[Entry], error) {
	params := engagementdb.ListUserReactionsParams{UserID: p.UserID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterCreated, params.AfterID = &req.Key.At, &req.Key.ID
	}
	rows, err := engagementdb.New(s.db.Primary).ListUserReactions(ctx, params)
	if err != nil {
		return page.Page[Entry]{}, err
	}
	out := make([]Entry, len(rows))
	for i, r := range rows {
		out[i] = Entry{ContentID: r.ContentID, SavedAt: r.CreatedAt}
	}
	return page.Build(out, req.Limit, entryCursor), nil
}

// ItemState is the caller's relationship to one item.
type ItemState struct {
	Liked          bool   `json:"liked"`
	Bookmarked     bool   `json:"bookmarked"`
	BookmarkListID *int64 `json:"bookmarkListId,omitempty"`
}

// State answers "have I liked / saved these?" for up to 100 items in two indexed lookups. It
// is what lets every page stay identical for all readers (and CDN-cacheable): the per-user
// bits are fetched separately by the page's client islands.
func (s *Service) State(ctx context.Context, p auth.Principal, ids []int64) (map[int64]ItemState, error) {
	if len(ids) > maxStateIDs {
		return nil, apperr.ValidationFailed(map[string]string{"ids": fmt.Sprintf("At most %d ids.", maxStateIDs)})
	}
	out := make(map[int64]ItemState, len(ids))
	for _, id := range ids {
		out[id] = ItemState{}
	}
	if len(ids) == 0 {
		return out, nil
	}
	q := engagementdb.New(s.db.Primary)
	liked, err := q.ReactedIDs(ctx, engagementdb.ReactedIDsParams{UserID: p.UserID, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, id := range liked {
		st := out[id]
		st.Liked = true
		out[id] = st
	}
	saved, err := q.BookmarkStates(ctx, engagementdb.BookmarkStatesParams{UserID: p.UserID, Ids: ids})
	if err != nil {
		return nil, err
	}
	for _, b := range saved {
		st := out[b.ContentID]
		st.Bookmarked, st.BookmarkListID = true, b.ListID
		out[b.ContentID] = st
	}
	return out, nil
}
