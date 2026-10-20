package content

import (
	"context"
	"strings"
	"time"

	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/platform/db"
)

// Indexable is what content exposes to search. Search builds its own documents from it, so
// content does not depend on the search engine or its document shape.
type Indexable struct {
	ID           int64
	Type         Type
	Locale       string
	Slug         string
	Title        string
	Summary      string
	Text         string   // derived plain text (no code), from content_render
	Tags         []string // names
	CategoryPath string   // URL form, e.g. dsa/algorithms
	CategoryName string   // the category's display name, e.g. "Algorithms"
	Access       string
	PublishedAt  time.Time
	UpdatedAt    time.Time
}

// Indexable returns the item's search fields, or ok=false when it must not be searchable
// (not live, deleted, or not PUBLIC: unlisted pages stay out of search like out of listings).
func (r *Reader) Indexable(ctx context.Context, id int64) (Indexable, bool, error) {
	row, err := contentdb.New(r.db.Replica).GetIndexable(ctx, id)
	if db.IsNoRows(err) {
		return Indexable{}, false, nil
	}
	if err != nil {
		return Indexable{}, false, err
	}
	if row.Status != string(StatusPublished) || row.Visibility != VisibilityPublic || row.DeletedAt != nil {
		return Indexable{}, false, nil
	}
	ix := Indexable{
		ID: row.ID, Type: Type(row.Type), Locale: row.Locale, Slug: row.Slug, Title: row.Title,
		Text: row.PlainText, Tags: row.TagNames, CategoryPath: strings.ReplaceAll(row.CategoryPath, ".", "/"),
		CategoryName: row.CategoryName, Access: row.Access, PublishedAt: *row.PublishedAt, UpdatedAt: *row.LastPublishedAt,
	}
	if row.Summary != nil {
		ix.Summary = *row.Summary
	}
	return ix, true, nil
}

// LiveIDsWithTagAfter walks the live ids carrying a tag, in order. Search re-checks each
// item's visibility, so non-public ones are dropped there.
func (r *Reader) LiveIDsWithTagAfter(ctx context.Context, tagID, after int64, limit int32) ([]int64, error) {
	return contentdb.New(r.db.Replica).ListLiveIDsWithTagAfter(ctx, contentdb.ListLiveIDsWithTagAfterParams{
		TagID: tagID, AfterID: after, RowLimit: limit,
	})
}

// LiveIDsInCategoryAfter walks the live public ids filed under a category's subtree, in order.
func (r *Reader) LiveIDsInCategoryAfter(ctx context.Context, categoryID, after int64, limit int32) ([]int64, error) {
	return contentdb.New(r.db.Replica).ListLiveIDsInCategoryAfter(ctx, contentdb.ListLiveIDsInCategoryAfterParams{
		CategoryID: categoryID, AfterID: after, RowLimit: limit,
	})
}

// LiveIDsAfter walks live public ids in order (for full reindexes).
func (r *Reader) LiveIDsAfter(ctx context.Context, after int64, limit int32) ([]int64, error) {
	return contentdb.New(r.db.Replica).ListLiveIDsAfter(ctx, contentdb.ListLiveIDsAfterParams{AfterID: after, RowLimit: limit})
}
