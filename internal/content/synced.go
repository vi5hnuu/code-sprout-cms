package content

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
)

// SyncedItem is an item mirrored from an outside source the owner controls (a YouTube upload).
type SyncedItem struct {
	Type    Type
	Locale  string // default "en"
	Title   string
	Slug    string // built from Title when empty
	Summary *string
	Body    string
	// FallbackSlug is used when Slug is taken by another item (e.g. title + external id).
	FallbackSlug string
}

// PublishSynced creates (id == 0) or refreshes item id from its source and publishes it, inside
// the caller's transaction so the caller's own rows change with it. It is the system path for
// mirrored content, like the scheduler's PublishDue: no review step (the owner's own channel is
// the authority) but the normal publish otherwise (revision, render, hooks, outbox events), so
// search, sitemaps and caches follow by themselves. ownerID is the author and audit actor.
// Returns the id and the revision now live.
func (s *Service) PublishSynced(ctx context.Context, tx pgx.Tx, ownerID, id int64, in SyncedItem) (int64, int32, error) {
	if in.Locale == "" {
		in.Locale = "en"
	}
	draft := DraftInput{Meta: Meta{Title: in.Title, Slug: in.Slug, Summary: in.Summary, Visibility: VisibilityPublic, Access: AccessFree}, Body: in.Body}
	normalize(in.Type, &draft)
	if err := validateDraft(draft); err != nil {
		return 0, 0, err
	}
	q := contentdb.New(tx)
	if id == 0 {
		created, err := s.insertSynced(ctx, tx, ownerID, in, &draft)
		if err != nil {
			return 0, 0, err
		}
		id = created
	}
	rev, err := s.appendRevision(ctx, q, id, ownerID, draft)
	if err != nil {
		return 0, 0, err
	}
	c, err := q.GetContentForUpdate(ctx, id)
	if err != nil {
		return 0, 0, err
	}
	meta, body, err := draftOf(ctx, q, c)
	if err != nil {
		return 0, 0, err
	}
	if err := s.publishLocked(ctx, tx, q, ownerID, c, meta, body); err != nil {
		return 0, 0, err
	}
	return id, rev, nil
}

// insertSynced creates the content row, trying the fallback slug when the first is taken. A
// savepoint keeps the outer transaction usable after the first attempt's unique violation.
func (s *Service) insertSynced(ctx context.Context, tx pgx.Tx, ownerID int64, in SyncedItem, draft *DraftInput) (int64, error) {
	for _, slug := range []string{draft.Slug, in.FallbackSlug} {
		if slug == "" {
			continue
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return 0, err
		}
		q := contentdb.New(sp)
		id, err := q.InsertContent(ctx, contentdb.InsertContentParams{
			Type: string(in.Type), Locale: in.Locale, Slug: slug, Title: draft.Title, Summary: draft.Summary,
			Visibility: draft.Visibility, Access: draft.Access, AuthorID: ownerID,
		})
		if err := mapContentErr(err); err != nil {
			_ = sp.Rollback(ctx)
			if isSlugTaken(err) {
				continue
			}
			return 0, err
		}
		if err := q.InsertStats(ctx, id); err != nil {
			_ = sp.Rollback(ctx)
			return 0, err
		}
		if err := sp.Commit(ctx); err != nil {
			return 0, err
		}
		draft.Slug = slug
		return id, audit.Record(ctx, tx, audit.Entry{ActorID: ownerID, Action: "content.create", Entity: "content", EntityID: id,
			Data: map[string]any{"type": in.Type, "slug": slug, "synced": true}})
	}
	return 0, slugTaken()
}

func isSlugTaken(err error) bool {
	ae, ok := apperr.As(err)
	return ok && ae.Code == slugTakenCode
}

// ArchiveSynced takes a mirrored item down when its source removed it (deleted, made private).
// History stays; a later sync that finds it again publishes it back. No-op when not live.
func (s *Service) ArchiveSynced(ctx context.Context, tx pgx.Tx, ownerID, id int64) error {
	q := contentdb.New(tx)
	c, err := q.GetContentForUpdate(ctx, id)
	if err != nil {
		return err
	}
	if Status(c.Status) != StatusPublished {
		return nil
	}
	if err := q.SetContentStatus(ctx, contentdb.SetContentStatusParams{ID: c.ID, Status: string(StatusArchived)}); err != nil {
		return err
	}
	if err := q.SetContentTagsPublishedAt(ctx, contentdb.SetContentTagsPublishedAtParams{ContentID: c.ID}); err != nil {
		return err
	}
	if err := s.onUnpublish(ctx, tx, c); err != nil {
		return err
	}
	return s.recordLive(ctx, tx, ownerID, c, "content.archive", outbox.TopicContentUnpublished, "")
}

// SetLinks replaces an item's links of one kind (e.g. a problem's VIDEO solution), for the
// domains built on content. Content still owns the table and its rules. It returns the targets
// linked before, so callers can refresh pages that list their backlinks.
func SetLinks(ctx context.Context, tx pgx.Tx, fromID int64, kind string, toIDs []int64) ([]int64, error) {
	q := contentdb.New(tx)
	previous, err := q.DeleteLinksOfKind(ctx, contentdb.DeleteLinksOfKindParams{FromID: fromID, Kind: kind})
	if err != nil || len(toIDs) == 0 {
		return previous, err
	}
	return previous, q.InsertLinks(ctx, contentdb.InsertLinksParams{FromID: fromID, Kind: kind, ToIds: toIDs})
}

// Touched records that a live item's public view changed outside its own publish (e.g. a
// problem gained a video link), so caches and search refresh like after a publish.
func Touched(ctx context.Context, tx pgx.Tx, id int64) error {
	c, err := contentdb.New(tx).GetContent(ctx, id)
	if err != nil {
		return err
	}
	return outbox.Publish(ctx, tx, outbox.TopicContentUpdated, id, outbox.ContentEvent{Type: c.Type, Locale: c.Locale, Slug: c.Slug})
}

// LinkTargets returns the items one item links to with kind.
func LinkTargets(ctx context.Context, tx pgx.Tx, fromID int64, kind string) ([]int64, error) {
	return contentdb.New(tx).ListLinkTargets(ctx, contentdb.ListLinkTargetsParams{FromID: fromID, Kind: kind})
}

// TouchedIfLive is Touched for each of ids that is published; the others have no page to refresh.
func TouchedIfLive(ctx context.Context, tx pgx.Tx, ids ...int64) error {
	for _, id := range ids {
		c, err := contentdb.New(tx).GetContent(ctx, id)
		if db.IsNoRows(err) {
			continue
		}
		if err != nil {
			return err
		}
		if Status(c.Status) != StatusPublished {
			continue
		}
		if err := outbox.Publish(ctx, tx, outbox.TopicContentUpdated, id, outbox.ContentEvent{Type: c.Type, Locale: c.Locale, Slug: c.Slug}); err != nil {
			return err
		}
	}
	return nil
}

// TitleMatch is a live problem whose normalised title equals one of the phrases asked about.
type TitleMatch struct {
	ID   int64
	Norm string
}

// LiveProblemsTitled finds live problems by exact normalised title (see norm_title).
func LiveProblemsTitled(ctx context.Context, tx pgx.Tx, phrases []string) ([]TitleMatch, error) {
	rows, err := contentdb.New(tx).LiveProblemsTitled(ctx, phrases)
	out := make([]TitleMatch, len(rows))
	for i, r := range rows {
		out[i] = TitleMatch{ID: r.ID, Norm: r.Norm}
	}
	return out, err
}

// WithLinkOfKind reports which of ids already link somewhere with kind.
func WithLinkOfKind(ctx context.Context, tx pgx.Tx, ids []int64, kind string) (map[int64]bool, error) {
	rows, err := contentdb.New(tx).WithLinkOfKind(ctx, contentdb.WithLinkOfKindParams{Ids: ids, Kind: kind})
	out := make(map[int64]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, err
}

// RevisionState is which revisions of an item readers and editors see.
type RevisionState struct {
	Live   int32 // the published revision; meaningful only when IsLive
	IsLive bool
	Draft  int32 // the newest revision in the editor
}

// Revisions returns the live and draft revision of an item.
func Revisions(ctx context.Context, tx pgx.Tx, id int64) (RevisionState, error) {
	c, err := contentdb.New(tx).GetContent(ctx, id)
	if err != nil {
		return RevisionState{}, err
	}
	st := RevisionState{Draft: c.DraftRevNo}
	if Status(c.Status) == StatusPublished && c.PublishedRevNo != nil {
		st.Live, st.IsLive = *c.PublishedRevNo, true
	}
	return st, nil
}
