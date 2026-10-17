// Package collection owns ordered groupings of content: readers' own lists (e.g. "Graph problems
// to revisit") and editorial collections (series, courses, study plans like "Top 150").
package collection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/collection/collectiondb"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
)

type Kind string

const (
	KindList      Kind = "LIST" // a reader's own list
	KindSeries    Kind = "SERIES"
	KindCourse    Kind = "COURSE"
	KindStudyPlan Kind = "STUDY_PLAN"
)

func (k Kind) editorial() bool { return k == KindSeries || k == KindCourse || k == KindStudyPlan }

const (
	maxListItems      = 1000
	maxEditorialItems = 500
	maxOwnLists       = 200
)

// Items hydrates content for display (implemented by content.Reader).
type Items interface {
	ItemsByID(ctx context.Context, ids []int64) ([]content.ListItem, error)
	LiveIDs(ctx context.Context, ids []int64) ([]int64, error)
}

type Service struct {
	db     *db.DB
	items  Items
	policy *access.Policy
}

func NewService(d *db.DB, items Items, policy *access.Policy) *Service {
	return &Service{db: d, items: items, policy: policy}
}

type Collection struct {
	ID          int64     `json:"id"`
	Kind        Kind      `json:"kind"`
	Slug        string    `json:"slug"`
	Title       string    `json:"title"`
	Description *string   `json:"description"`
	OwnerID     int64     `json:"ownerId"`
	Visibility  string    `json:"visibility"`
	ItemCount   *int32    `json:"itemCount,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Input struct {
	Kind        Kind    `json:"kind"`
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility"`
}

func fromRow(c collectiondb.Collection) Collection {
	return Collection{ID: c.ID, Kind: Kind(c.Kind), Slug: c.Slug, Title: c.Title, Description: c.Description,
		OwnerID: c.OwnerID, Visibility: c.Visibility, UpdatedAt: c.UpdatedAt}
}

var errNotFound = apperr.NotFound("collection.not_found", "No such collection.")

func validate(in *Input) error {
	f := map[string]string{}
	in.Title = strings.TrimSpace(in.Title)
	if n := len([]rune(in.Title)); n == 0 || n > 120 {
		f["title"] = "Title is required and at most 120 characters."
	}
	if in.Description != nil && len([]rune(*in.Description)) > 1000 {
		f["description"] = "Description is at most 1000 characters."
	}
	if in.Visibility == "" {
		in.Visibility = "PRIVATE"
	}
	switch in.Visibility {
	case "PUBLIC", "UNLISTED", "PRIVATE":
	default:
		f["visibility"] = "visibility must be PUBLIC, UNLISTED or PRIVATE."
	}
	if in.Slug == "" {
		in.Slug = content.Slugify(in.Title)
	}
	if in.Slug == "" {
		in.Slug = "list" // reader lists are addressed by id; the slug only matters for editorial ones
	}
	if len(f) > 0 {
		return apperr.ValidationFailed(f)
	}
	return nil
}

// Create makes a reader list (any signed-in user) or an editorial collection (site-wide EDITOR).
func (s *Service) Create(ctx context.Context, p auth.Principal, in Input) (Collection, error) {
	if in.Kind == "" {
		in.Kind = KindList
	}
	if in.Kind != KindList && !in.Kind.editorial() {
		return Collection{}, apperr.ValidationFailed(map[string]string{"kind": "kind must be LIST, SERIES, COURSE or STUDY_PLAN."})
	}
	if err := validate(&in); err != nil {
		return Collection{}, err
	}
	q := collectiondb.New(s.db.Primary)
	if in.Kind.editorial() {
		if err := s.policy.Require(ctx, p, access.RoleEditor, nil); err != nil {
			return Collection{}, err
		}
	} else {
		own, err := q.ListOwnerCollections(ctx, p.UserID)
		if err != nil {
			return Collection{}, err
		}
		if len(own) >= maxOwnLists {
			return Collection{}, apperr.Conflict("collection.limit", fmt.Sprintf("You can have up to %d lists.", maxOwnLists))
		}
	}
	c, err := q.InsertCollection(ctx, collectiondb.InsertCollectionParams{
		Kind: string(in.Kind), Slug: in.Slug, Title: in.Title, Description: in.Description, OwnerID: p.UserID, Visibility: in.Visibility,
	})
	if cv, ok := db.ConstraintViolation(err); ok && cv == "uq_collection_slug" {
		return Collection{}, apperr.Conflict("collection.slug_taken", "A collection with this slug already exists.")
	}
	if err != nil {
		return Collection{}, err
	}
	return fromRow(c), nil
}

// editable loads a collection the caller may change: their own list, or (for editorial ones)
// any site-wide editor.
func (s *Service) editable(ctx context.Context, q *collectiondb.Queries, p auth.Principal, id int64) (collectiondb.Collection, error) {
	c, err := q.GetCollectionForUpdate(ctx, id)
	if db.IsNoRows(err) {
		return c, errNotFound
	}
	if err != nil {
		return c, err
	}
	if Kind(c.Kind).editorial() {
		return c, s.policy.Require(ctx, p, access.RoleEditor, nil)
	}
	if c.OwnerID != p.UserID {
		return c, errNotFound
	}
	return c, nil
}

func (s *Service) Update(ctx context.Context, p auth.Principal, id int64, in Input) (Collection, error) {
	if err := validate(&in); err != nil {
		return Collection{}, err
	}
	var out collectiondb.Collection
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := collectiondb.New(tx)
		if _, err := s.editable(ctx, q, p, id); err != nil {
			return err
		}
		var err error
		out, err = q.UpdateCollection(ctx, collectiondb.UpdateCollectionParams{ID: id, Title: in.Title, Description: in.Description, Visibility: in.Visibility})
		return err
	})
	return fromRow(out), err
}

func (s *Service) Delete(ctx context.Context, p auth.Principal, id int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := collectiondb.New(tx)
		if _, err := s.editable(ctx, q, p, id); err != nil {
			return err
		}
		return q.DeleteCollection(ctx, id)
	})
}

// AddItem appends a live item; adding one that is already there is a no-op.
func (s *Service) AddItem(ctx context.Context, p auth.Principal, id, contentID int64) error {
	live, err := s.items.LiveIDs(ctx, []int64{contentID})
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return apperr.NotFound("content.not_found", "No such content.")
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := collectiondb.New(tx)
		c, err := s.editable(ctx, q, p, id)
		if err != nil {
			return err
		}
		n, err := q.CountItems(ctx, id)
		if err != nil {
			return err
		}
		if limit := limitFor(Kind(c.Kind)); int(n) >= limit {
			return apperr.Conflict("collection.full", fmt.Sprintf("A collection holds at most %d items.", limit))
		}
		if _, err := q.AddItem(ctx, collectiondb.AddItemParams{CollectionID: id, ContentID: contentID}); err != nil {
			return err
		}
		return q.TouchCollection(ctx, id)
	})
}

func (s *Service) RemoveItem(ctx context.Context, p auth.Principal, id, contentID int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := collectiondb.New(tx)
		if _, err := s.editable(ctx, q, p, id); err != nil {
			return err
		}
		if _, err := q.RemoveItem(ctx, collectiondb.RemoveItemParams{CollectionID: id, ContentID: contentID}); err != nil {
			return err
		}
		return q.TouchCollection(ctx, id)
	})
}

// Reorder sets the full order (courses and study plans are sequences). The given ids must be
// exactly the collection's current items.
func (s *Service) Reorder(ctx context.Context, p auth.Principal, id int64, contentIDs []int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := collectiondb.New(tx)
		c, err := s.editable(ctx, q, p, id)
		if err != nil {
			return err
		}
		current, err := q.ListItems(ctx, collectiondb.ListItemsParams{CollectionID: id, RowLimit: int32(limitFor(Kind(c.Kind)) + 1)})
		if err != nil {
			return err
		}
		if !sameSet(current, contentIDs) {
			return apperr.ValidationFailed(map[string]string{"contentIds": "List every item in the collection exactly once."})
		}
		if err := q.ClearItems(ctx, id); err != nil {
			return err
		}
		for i, cid := range contentIDs {
			if err := q.InsertItemAt(ctx, collectiondb.InsertItemAtParams{CollectionID: id, ContentID: cid, Position: float64(i + 1)}); err != nil {
				return err
			}
		}
		return q.TouchCollection(ctx, id)
	})
}

func limitFor(k Kind) int {
	if k.editorial() {
		return maxEditorialItems
	}
	return maxListItems
}

func sameSet(current []collectiondb.ListItemsRow, ids []int64) bool {
	if len(current) != len(ids) {
		return false
	}
	have := make(map[int64]bool, len(current))
	for _, c := range current {
		have[c.ContentID] = true
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !have[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Service) Mine(ctx context.Context, p auth.Principal) ([]Collection, error) {
	rows, err := collectiondb.New(s.db.Primary).ListOwnerCollections(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]Collection, len(rows))
	for i, r := range rows {
		c := Collection{ID: r.ID, Kind: Kind(r.Kind), Slug: r.Slug, Title: r.Title, Description: r.Description,
			OwnerID: r.OwnerID, Visibility: r.Visibility, UpdatedAt: r.UpdatedAt}
		n := r.ItemCount
		c.ItemCount = &n
		out[i] = c
	}
	return out, nil
}

// Containing lists which of the caller's lists hold an item (for an "add to list" menu).
func (s *Service) Containing(ctx context.Context, p auth.Principal, contentID int64) ([]int64, error) {
	return collectiondb.New(s.db.Primary).CollectionsContaining(ctx, collectiondb.CollectionsContainingParams{OwnerID: p.UserID, ContentID: contentID})
}

type View struct {
	Collection
	Items      []content.ListItem `json:"items"`
	NextCursor *string            `json:"nextCursor"`
}

// PublicBySlug serves an editorial collection to readers.
func (s *Service) PublicBySlug(ctx context.Context, kind Kind, slug string, req page.Request[float64]) (View, error) {
	if !kind.editorial() {
		return View{}, errNotFound
	}
	c, err := collectiondb.New(s.db.Replica).GetCollectionBySlug(ctx, collectiondb.GetCollectionBySlugParams{Kind: string(kind), Slug: slug})
	if db.IsNoRows(err) || (err == nil && c.Visibility == "PRIVATE") {
		return View{}, errNotFound
	}
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, c, req)
}

// View shows a list to its owner, or anyone's PUBLIC/UNLISTED list by id.
func (s *Service) View(ctx context.Context, p *auth.Principal, id int64, req page.Request[float64]) (View, error) {
	c, err := collectiondb.New(s.db.Replica).GetCollection(ctx, id)
	if db.IsNoRows(err) {
		return View{}, errNotFound
	}
	if err != nil {
		return View{}, err
	}
	owner := p != nil && p.UserID == c.OwnerID
	if c.Visibility == "PRIVATE" && !owner {
		return View{}, errNotFound
	}
	return s.view(ctx, c, req)
}

func (s *Service) view(ctx context.Context, c collectiondb.Collection, req page.Request[float64]) (View, error) {
	rows, err := collectiondb.New(s.db.Replica).ListItems(ctx, collectiondb.ListItemsParams{
		CollectionID: c.ID, AfterPosition: req.Key, RowLimit: int32(req.Limit + 1),
	})
	if err != nil {
		return View{}, err
	}
	built := page.Build(rows, req.Limit, func(r collectiondb.ListItemsRow) float64 { return r.Position })
	ids := make([]int64, len(built.Items))
	for i, r := range built.Items {
		ids[i] = r.ContentID
	}
	items, err := s.items.ItemsByID(ctx, ids)
	if err != nil {
		return View{}, err
	}
	return View{Collection: fromRow(c), Items: items, NextCursor: built.NextCursor}, nil
}
