package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/taxonomy"
)

// Assets resolves asset:<id> references (implemented by asset.Service).
type Assets interface {
	Describe(ctx context.Context, ids []int64) (map[int64]asset.File, error)
}

// Taxonomy validates category and tag ids (implemented by taxonomy.Service).
type Taxonomy interface {
	Get(ctx context.Context, id int64) (taxonomy.Category, error)
	ExistingTagIDs(ctx context.Context, ids []int64) ([]int64, error)
}

type Service struct {
	db       *db.DB
	policy   *access.Policy
	assets   Assets
	taxonomy Taxonomy
	hooks    map[Type]TypeHooks
}

func NewService(d *db.DB, policy *access.Policy, assets Assets, tax Taxonomy) *Service {
	return &Service{db: d, policy: policy, assets: assets, taxonomy: tax}
}

var errNotFound = apperr.NotFound("content.not_found", "No such content.")

// Create starts a new article, blog or topic page with its first draft revision.
func (s *Service) Create(ctx context.Context, p auth.Principal, in CreateInput) (Draft, error) {
	if !studioTypes[in.Type] {
		return Draft{}, apperr.ValidationFailed(map[string]string{"type": "type must be ARTICLE, BLOG or TOPIC_PAGE."})
	}
	return s.create(ctx, p, in, nil)
}

func (s *Service) create(ctx context.Context, p auth.Principal, in CreateInput, ext func(ctx context.Context, tx pgx.Tx, id int64) error) (Draft, error) {
	if err := access.RequireAuthor(p); err != nil {
		return Draft{}, err
	}
	if in.Locale == "" {
		in.Locale = "en"
	}
	normalize(in.Type, &in.DraftInput)
	if err := errors.Join(validateCreate(in), validateDraft(in.DraftInput)); err != nil {
		return Draft{}, mergeValidation(err)
	}
	if err := s.checkTaxonomy(ctx, in.Meta); err != nil {
		return Draft{}, err
	}

	var id int64
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		var err error
		id, err = q.InsertContent(ctx, contentdb.InsertContentParams{
			Type: string(in.Type), Locale: in.Locale, Slug: in.Slug, Title: in.Title, Summary: in.Summary,
			Visibility: in.Visibility, Access: in.Access, AuthorID: p.UserID, PrimaryCategoryID: in.CategoryID,
		})
		if err != nil {
			return mapContentErr(err)
		}
		if _, err := s.appendRevision(ctx, q, id, p.UserID, in.DraftInput); err != nil {
			return err
		}
		if err := q.InsertContentTags(ctx, contentdb.InsertContentTagsParams{TagIds: in.TagIDs, ContentID: id}); err != nil {
			return err
		}
		if err := q.InsertStats(ctx, id); err != nil {
			return err
		}
		if ext != nil {
			if err := ext(ctx, tx, id); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "content.create", Entity: "content", EntityID: id,
			Data: map[string]any{"type": in.Type, "slug": in.Slug}})
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Get(ctx, p, id)
}

// SaveDraft replaces the draft with a new revision. ifMatch must equal the draft revision
// the editor started from; otherwise someone saved in between and the caller must reload
// (412) instead of silently overwriting their work.
func (s *Service) SaveDraft(ctx context.Context, p auth.Principal, id int64, ifMatch int32, in DraftInput) (Draft, error) {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		c, err := s.lockForEdit(ctx, q, p, id)
		if err != nil {
			return err
		}
		if c.DraftRevNo != ifMatch {
			return staleRevision(c.DraftRevNo)
		}
		_, err = s.saveLocked(ctx, q, p, c, in)
		return err
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Get(ctx, p, id)
}

// saveLocked validates and stores a new draft revision for a locked row. It returns false
// when the draft is unchanged (no revision is written for a no-op save).
func (s *Service) saveLocked(ctx context.Context, q *contentdb.Queries, p auth.Principal, c contentdb.Content, in DraftInput) (bool, error) {
	t := Type(c.Type)
	normalize(t, &in)
	if err := validateDraft(in); err != nil {
		return false, err
	}
	if err := s.checkTaxonomy(ctx, in.Meta); err != nil {
		return false, err
	}
	taken, err := q.SlugTakenByOther(ctx, contentdb.SlugTakenByOtherParams{Type: c.Type, Locale: c.Locale, Slug: in.Slug, ID: c.ID})
	if err != nil {
		return false, err
	}
	if taken {
		return false, slugTaken()
	}

	cur, curMeta, _, err := revisionOf(ctx, q, c.ID, c.DraftRevNo)
	if err != nil {
		return false, err
	}
	// Compare canonical forms: jsonb reorders keys, so the stored bytes never match ours.
	sum := sha256.Sum256([]byte(in.Body))
	if bytes.Equal(cur.BodySha256, sum[:]) && bytes.Equal(marshalMeta(curMeta), marshalMeta(in.Meta)) {
		return false, nil
	}

	if _, err := s.appendRevision(ctx, q, c.ID, p.UserID, in); err != nil {
		return false, err
	}
	if Status(c.Status) == StatusDraft {
		if err := q.MirrorDraftFace(ctx, contentdb.MirrorDraftFaceParams{
			ID: c.ID, Slug: in.Slug, Title: in.Title, Summary: in.Summary, PrimaryCategoryID: in.CategoryID,
			Visibility: in.Visibility, Access: in.Access,
		}); err != nil {
			return false, mapContentErr(err)
		}
		if err := replaceTags(ctx, q, c.ID, in.TagIDs, nil); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Service) appendRevision(ctx context.Context, q *contentdb.Queries, contentID, editorID int64, in DraftInput) (int32, error) {
	rev, err := q.AllocateRevNo(ctx, contentID)
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256([]byte(in.Body))
	body := in.Body
	if err := q.InsertRevision(ctx, contentdb.InsertRevisionParams{
		ContentID: contentID, RevNo: rev, BodyMd: &body, BodySha256: sum[:], Meta: marshalMeta(in.Meta),
		EditorID: editorID, Message: in.Message,
	}); err != nil {
		return 0, err
	}
	return rev, q.SetDraftRevision(ctx, contentdb.SetDraftRevisionParams{ID: contentID, RevNo: rev})
}

// replaceTags sets the item's tags. publishedAt is nil while it is not live, which keeps it
// out of tag listings.
func replaceTags(ctx context.Context, q *contentdb.Queries, id int64, tagIDs []int64, publishedAt *time.Time) error {
	if err := q.DeleteContentTags(ctx, id); err != nil {
		return err
	}
	return q.InsertContentTags(ctx, contentdb.InsertContentTagsParams{TagIds: tagIDs, ContentID: id, PublishedAt: publishedAt})
}

func (s *Service) checkTaxonomy(ctx context.Context, m Meta) error {
	if m.CategoryID != nil {
		if _, err := s.taxonomy.Get(ctx, *m.CategoryID); err != nil {
			if e, ok := apperr.As(err); ok && e.Kind == apperr.KindNotFound {
				return apperr.ValidationFailed(map[string]string{"categoryId": "No such category."})
			}
			return err
		}
	}
	if len(m.TagIDs) > 0 {
		found, err := s.taxonomy.ExistingTagIDs(ctx, m.TagIDs)
		if err != nil {
			return err
		}
		if len(found) != len(m.TagIDs) {
			return apperr.ValidationFailed(map[string]string{"tagIds": fmt.Sprintf("Unknown tag id(s): %v.", missing(m.TagIDs, found))})
		}
	}
	return nil
}

// Get returns the studio view: the current draft with its Markdown issues.
func (s *Service) Get(ctx context.Context, p auth.Principal, id int64) (Draft, error) {
	q := contentdb.New(s.db.Primary)
	c, err := q.GetContent(ctx, id)
	if db.IsNoRows(err) {
		return Draft{}, errNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	meta, body, err := draftOf(ctx, q, c)
	if err != nil {
		return Draft{}, err
	}
	if err := s.requireView(ctx, p, c, meta); err != nil {
		return Draft{}, err
	}
	return toDraft(c, meta, body), nil
}

func toDraft(c contentdb.Content, meta Meta, body string) Draft {
	d := Draft{
		ID: c.ID, Type: Type(c.Type), Locale: c.Locale, Status: Status(c.Status), ReviewState: ReviewState(c.ReviewState),
		ScheduledAt: c.ScheduledAt, AuthorID: c.AuthorID, DraftRevNo: c.DraftRevNo,
		HasUnpublishedChanges: c.PublishedRevNo == nil || *c.PublishedRevNo != c.DraftRevNo,
		PublishedAt:           c.PublishedAt, LastPublishedAt: c.LastPublishedAt, UpdatedAt: c.UpdatedAt,
		Meta: meta, Body: body, Issues: markdown.Analyze([]byte(body)).Issues,
	}
	if c.PublishedRevNo != nil {
		d.Live = &LiveFace{Slug: c.Slug, Title: c.Title, RevNo: *c.PublishedRevNo}
	}
	return d
}

func draftOf(ctx context.Context, q *contentdb.Queries, c contentdb.Content) (Meta, string, error) {
	_, meta, body, err := revisionOf(ctx, q, c.ID, c.DraftRevNo)
	return meta, body, err
}

func revisionOf(ctx context.Context, q *contentdb.Queries, id int64, rev int32) (contentdb.ContentRevision, Meta, string, error) {
	r, err := q.GetRevision(ctx, contentdb.GetRevisionParams{ContentID: id, RevNo: rev})
	if db.IsNoRows(err) {
		return r, Meta{}, "", apperr.NotFound("content.revision_not_found", "No such revision.")
	}
	if err != nil {
		return r, Meta{}, "", err
	}
	// Only old, superseded revisions are ever archived; draft and live revisions keep bodies.
	if r.BodyMd == nil {
		return r, Meta{}, "", apperr.Conflict("content.revision_archived", "This revision is archived and cannot be loaded here.")
	}
	var m Meta
	if err := json.Unmarshal(r.Meta, &m); err != nil {
		return r, Meta{}, "", fmt.Errorf("content %d rev %d: meta: %w", id, rev, err)
	}
	return r, m, *r.BodyMd, nil
}

// ListMine lists the caller's own items, most recently updated first.
func (s *Service) ListMine(ctx context.Context, p auth.Principal, req page.Request[ListCursor]) (page.Page[Summary], error) {
	params := contentdb.ListAuthorContentParams{AuthorID: p.UserID, RowLimit: int32(req.Limit + 1)}
	if req.Key != nil {
		params.AfterUpdated, params.AfterID = &req.Key.UpdatedAt, &req.Key.ID
	}
	rows, err := contentdb.New(s.db.Primary).ListAuthorContent(ctx, params)
	if err != nil {
		return page.Page[Summary]{}, err
	}
	return page.Build(summaries(rows), req.Limit, cursorOf), nil
}

// ReviewQueue lists items waiting on the caller as a reviewer (IN_REVIEW) or editor
// (APPROVED, ready to publish), oldest first, limited to the categories they cover.
func (s *Service) ReviewQueue(ctx context.Context, p auth.Principal, state ReviewState, req page.Request[ListCursor]) (page.Page[Summary], error) {
	role := access.RoleReviewer
	switch state {
	case ReviewInReview:
	case ReviewApproved:
		role = access.RoleEditor
	default:
		return page.Page[Summary]{}, apperr.ValidationFailed(map[string]string{"state": "state must be IN_REVIEW or APPROVED."})
	}
	params := contentdb.ListReviewQueueParams{
		States: []string{string(state)}, IsAdmin: p.IsAdmin, UserID: p.UserID,
		Roles: access.RolesSatisfying(role), RowLimit: int32(req.Limit + 1),
	}
	if req.Key != nil {
		params.AfterUpdated, params.AfterID = &req.Key.UpdatedAt, &req.Key.ID
	}
	rows, err := contentdb.New(s.db.Primary).ListReviewQueue(ctx, params)
	if err != nil {
		return page.Page[Summary]{}, err
	}
	return page.Build(summaries(rows), req.Limit, cursorOf), nil
}

func summaries(rows []contentdb.Content) []Summary {
	out := make([]Summary, len(rows))
	for i, c := range rows {
		out[i] = Summary{
			ID: c.ID, Type: Type(c.Type), Locale: c.Locale, Title: c.Title, Slug: c.Slug, Status: Status(c.Status),
			ReviewState: ReviewState(c.ReviewState), AuthorID: c.AuthorID, DraftRevNo: c.DraftRevNo, UpdatedAt: c.UpdatedAt,
		}
	}
	return out
}

func cursorOf(s Summary) ListCursor { return ListCursor{UpdatedAt: s.UpdatedAt, ID: s.ID} }

// Revisions lists history newest first; the cursor is the last revision number seen.
func (s *Service) Revisions(ctx context.Context, p auth.Principal, id int64, req page.Request[int32]) (page.Page[Revision], error) {
	q := contentdb.New(s.db.Primary)
	if _, err := s.viewable(ctx, q, p, id); err != nil {
		return page.Page[Revision]{}, err
	}
	rows, err := q.ListRevisions(ctx, contentdb.ListRevisionsParams{ContentID: id, BeforeRev: req.Key, RowLimit: int32(req.Limit + 1)})
	if err != nil {
		return page.Page[Revision]{}, err
	}
	out := make([]Revision, len(rows))
	for i, r := range rows {
		out[i] = Revision{RevNo: r.RevNo, EditorID: r.EditorID, Message: r.Message, CreatedAt: r.CreatedAt, Archived: r.Archived}
	}
	return page.Build(out, req.Limit, func(r Revision) int32 { return r.RevNo }), nil
}

func (s *Service) RevisionDetail(ctx context.Context, p auth.Principal, id int64, rev int32) (RevisionDetail, error) {
	q := contentdb.New(s.db.Primary)
	if _, err := s.viewable(ctx, q, p, id); err != nil {
		return RevisionDetail{}, err
	}
	r, meta, body, err := revisionOf(ctx, q, id, rev)
	if err != nil {
		return RevisionDetail{}, err
	}
	return RevisionDetail{
		Revision: Revision{RevNo: r.RevNo, EditorID: r.EditorID, Message: r.Message, CreatedAt: r.CreatedAt},
		Meta:     meta, Body: body,
	}, nil
}

// Restore makes an old revision the draft again, as a new revision (history is never
// rewritten).
func (s *Service) Restore(ctx context.Context, p auth.Principal, id int64, rev int32, ifMatch int32) (Draft, error) {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		c, err := s.lockForEdit(ctx, q, p, id)
		if err != nil {
			return err
		}
		if c.DraftRevNo != ifMatch {
			return staleRevision(c.DraftRevNo)
		}
		_, meta, body, err := revisionOf(ctx, q, id, rev)
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("Restored revision %d", rev)
		_, err = s.saveLocked(ctx, q, p, c, DraftInput{Meta: meta, Body: body, Message: &msg})
		return err
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Get(ctx, p, id)
}

// ── permissions ──────────────────────────────────────────────────────────────

// scopeOf is the category whose editors/reviewers govern the item: the draft's category
// (where it is going) or, if none yet, the live one.
func scopeOf(c contentdb.Content, m Meta) *int64 {
	if m.CategoryID != nil {
		return m.CategoryID
	}
	return c.PrimaryCategoryID
}

func (s *Service) requireView(ctx context.Context, p auth.Principal, c contentdb.Content, m Meta) error {
	if c.AuthorID == p.UserID {
		return nil
	}
	ok, err := s.policy.Has(ctx, p, access.RoleReviewer, scopeOf(c, m))
	if err != nil {
		return err
	}
	if !ok {
		return errNotFound // others' drafts are invisible, not "forbidden"
	}
	return nil
}

func (s *Service) viewable(ctx context.Context, q *contentdb.Queries, p auth.Principal, id int64) (contentdb.Content, error) {
	c, err := q.GetContent(ctx, id)
	if db.IsNoRows(err) {
		return c, errNotFound
	}
	if err != nil {
		return c, err
	}
	meta, _, err := draftOf(ctx, q, c)
	if err != nil {
		return c, err
	}
	return c, s.requireView(ctx, p, c, meta)
}

// lockForEdit locks the row and checks the caller may change the draft: the author, unless it
// is waiting on review (then only editors, so what was approved is what gets published).
func (s *Service) lockForEdit(ctx context.Context, q *contentdb.Queries, p auth.Principal, id int64) (contentdb.Content, error) {
	c, meta, err := s.lock(ctx, q, p, id)
	if err != nil {
		return c, err
	}
	isEditor, err := s.policy.Has(ctx, p, access.RoleEditor, scopeOf(c, meta))
	if err != nil {
		return c, err
	}
	if isEditor {
		return c, nil
	}
	if c.AuthorID != p.UserID || p.IsGuest {
		return c, apperr.Forbidden("content.not_editable", "Only the author or an editor can change this.")
	}
	if ReviewState(c.ReviewState).locked() {
		return c, apperr.Conflict("content.in_review", "Withdraw it from review before editing.")
	}
	return c, nil
}

// lock loads and locks the row plus its draft meta, enforcing visibility.
func (s *Service) lock(ctx context.Context, q *contentdb.Queries, p auth.Principal, id int64) (contentdb.Content, Meta, error) {
	c, err := q.GetContentForUpdate(ctx, id)
	if db.IsNoRows(err) {
		return c, Meta{}, errNotFound
	}
	if err != nil {
		return c, Meta{}, err
	}
	meta, _, err := draftOf(ctx, q, c)
	if err != nil {
		return c, Meta{}, err
	}
	return c, meta, s.requireView(ctx, p, c, meta)
}

// ── errors ───────────────────────────────────────────────────────────────────

func staleRevision(current int32) error {
	return &apperr.Error{
		Kind: apperr.KindPreconditionFailed, Code: "content.stale_revision",
		Detail:  "Someone saved a newer version. Reload to see it before saving again.",
		Details: map[string]any{"currentRevNo": current},
	}
}

func slugTaken() error {
	return apperr.Conflict("content.slug_taken", "Another item already uses this slug.")
}

func mapContentErr(err error) error {
	if c, ok := db.ConstraintViolation(err); ok && c == "uq_content_slug" {
		return slugTaken()
	}
	return err
}

// mergeValidation folds several ValidationFailed errors into one response.
func mergeValidation(err error) error {
	fields := map[string]string{}
	var other error
	for _, e := range unwrapAll(err) {
		if ae, ok := apperr.As(e); ok && ae.Kind == apperr.KindInvalid && ae.Fields != nil {
			for k, v := range ae.Fields {
				fields[k] = v
			}
			continue
		}
		other = e
	}
	if other != nil {
		return other
	}
	return apperr.ValidationFailed(fields)
}

func unwrapAll(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

func missing(want, found []int64) []int64 {
	have := make(map[int64]bool, len(found))
	for _, id := range found {
		have[id] = true
	}
	var out []int64
	for _, id := range want {
		if !have[id] {
			out = append(out, id)
		}
	}
	return out
}
