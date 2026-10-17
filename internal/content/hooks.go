package content

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
)

// TypeHooks let a domain that builds on content (problems, quizzes) take part in its
// lifecycle without content knowing about it. Hooks run inside the workflow transaction, so
// the type's own tables change atomically with the content row.
type TypeHooks struct {
	// Check reports type-specific publish blockers (e.g. a problem without test cases).
	Check func(ctx context.Context, tx pgx.Tx, contentID int64) ([]PublishIssue, error)
	// OnPublish runs after the item went live (first publish, update or republish).
	OnPublish func(ctx context.Context, tx pgx.Tx, live LiveInfo) error
	// OnUnpublish runs when the item stops being live (archive or delete).
	OnUnpublish func(ctx context.Context, tx pgx.Tx, contentID int64) error
}

// LiveInfo is what a hook needs to know about the item that just went live.
type LiveInfo struct {
	ID          int64
	Locale      string
	Visibility  string
	PublishedAt time.Time
	TagIDs      []int64
}

// RegisterType installs hooks for a type and enables CreateTyped for it. Call during wiring,
// before serving requests (registration is not synchronised with request handling).
func (s *Service) RegisterType(t Type, h TypeHooks) {
	if s.hooks == nil {
		s.hooks = map[Type]TypeHooks{}
	}
	s.hooks[t] = h
}

// CreateTyped creates an item of a registered type; ext inserts the type's own row in the
// same transaction.
func (s *Service) CreateTyped(ctx context.Context, p auth.Principal, in CreateInput, ext func(ctx context.Context, tx pgx.Tx, id int64) error) (Draft, error) {
	if _, ok := s.hooks[in.Type]; !ok {
		return Draft{}, apperr.ValidationFailed(map[string]string{"type": "This type is not available here."})
	}
	return s.create(ctx, p, in, ext)
}

// RequireStructureEdit authorises changes to a type's structured data (a problem's tests,
// samples, limits). While the item has never been live its author may edit them; once it has
// been published, those changes affect readers immediately, so only editors may.
func (s *Service) RequireStructureEdit(ctx context.Context, p auth.Principal, id int64, t Type) error {
	q := contentdb.New(s.db.Primary)
	c, err := q.GetContent(ctx, id)
	if db.IsNoRows(err) || (err == nil && Type(c.Type) != t) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	meta, _, err := draftOf(ctx, q, c)
	if err != nil {
		return err
	}
	if err := s.requireView(ctx, p, c, meta); err != nil {
		return err
	}
	isEditor, err := s.policy.Has(ctx, p, access.RoleEditor, scopeOf(c, meta))
	if err != nil || isEditor {
		return err
	}
	if Status(c.Status) != StatusDraft || c.AuthorID != p.UserID || p.IsGuest {
		return apperr.Forbidden("content.not_editable", "Only an editor can change this once it has been published.")
	}
	if ReviewState(c.ReviewState).locked() {
		return apperr.Conflict("content.in_review", "Withdraw it from review before editing.")
	}
	return nil
}

func (s *Service) typeIssues(ctx context.Context, tx pgx.Tx, c contentdb.Content) ([]PublishIssue, error) {
	h, ok := s.hooks[Type(c.Type)]
	if !ok || h.Check == nil {
		return nil, nil
	}
	return h.Check(ctx, tx, c.ID)
}

func (s *Service) onPublish(ctx context.Context, tx pgx.Tx, live contentdb.Content, tagIDs []int64) error {
	h, ok := s.hooks[Type(live.Type)]
	if !ok || h.OnPublish == nil {
		return nil
	}
	return h.OnPublish(ctx, tx, LiveInfo{ID: live.ID, Locale: live.Locale, Visibility: live.Visibility, PublishedAt: *live.PublishedAt, TagIDs: tagIDs})
}

func (s *Service) onUnpublish(ctx context.Context, tx pgx.Tx, c contentdb.Content) error {
	h, ok := s.hooks[Type(c.Type)]
	if !ok || h.OnUnpublish == nil {
		return nil
	}
	return h.OnUnpublish(ctx, tx, c.ID)
}
