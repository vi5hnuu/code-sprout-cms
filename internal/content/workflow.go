package content

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/audit"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content/contentdb"
	"code-sprout-cms/internal/markdown"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/apperr"
)

// Workflow:
//
//	author:   save draft ─► submit ─► (IN_REVIEW) ◄─ withdraw
//	reviewer: approve ─► APPROVED     request changes ─► CHANGES_REQUESTED ─► author saves, resubmits
//	editor:   publish (from any review state) | schedule (APPROVED ─► SCHEDULED) | archive
//
// The live page is untouched by all of it until publish copies the draft into the public face.

const (
	maxScheduleAhead = 365 * 24 * time.Hour
	maxReviewNotes   = 200
)

func invalidState(detail string) error { return apperr.Conflict("content.invalid_state", detail) }

// Submit sends the draft to reviewers. It must already be publishable, so reviewers judge
// content, not missing alt text.
func (s *Service) Submit(ctx context.Context, p auth.Principal, id int64) (Draft, error) {
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, body string) error {
		if err := s.requireAuthorOrEditor(ctx, p, c, meta); err != nil {
			return err
		}
		if rs := ReviewState(c.ReviewState); rs != ReviewNone && rs != ReviewChangesRequested {
			return invalidState("It is already submitted.")
		}
		if c.PublishedRevNo != nil && *c.PublishedRevNo == c.DraftRevNo && Status(c.Status) == StatusPublished {
			return invalidState("There are no changes to review.")
		}
		if _, err := s.checkPublishable(ctx, tx, q, c, meta, body); err != nil {
			return err
		}
		return s.setReview(ctx, tx, q, p.UserID, c, ReviewInReview, nil, "content.submit")
	})
}

// Withdraw takes the draft back from review (or cancels a schedule) so it can be edited.
func (s *Service) Withdraw(ctx context.Context, p auth.Principal, id int64) (Draft, error) {
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, _ string) error {
		if err := s.requireAuthorOrEditor(ctx, p, c, meta); err != nil {
			return err
		}
		if !ReviewState(c.ReviewState).locked() {
			return invalidState("It is not waiting on review or a schedule.")
		}
		return s.setReview(ctx, tx, q, p.UserID, c, ReviewNone, nil, "content.withdraw")
	})
}

// Review records a reviewer's decision on the current draft revision.
func (s *Service) Review(ctx context.Context, p auth.Principal, id int64, in ReviewInput) (Draft, error) {
	if in.Decision != DecisionApproved && in.Decision != DecisionChangesRequested {
		return Draft{}, apperr.ValidationFailed(map[string]string{"decision": "decision must be APPROVED or CHANGES_REQUESTED."})
	}
	if len(in.Notes) > maxReviewNotes {
		return Draft{}, apperr.ValidationFailed(map[string]string{"notes": fmt.Sprintf("At most %d notes.", maxReviewNotes)})
	}
	for i, n := range in.Notes {
		if strings.TrimSpace(n.Body) == "" || len(n.Anchor) == 0 {
			return Draft{}, apperr.ValidationFailed(map[string]string{fmt.Sprintf("notes[%d]", i): "Each note needs an anchor and a body."})
		}
	}
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, _ string) error {
		if err := s.policy.Require(ctx, p, access.RoleReviewer, scopeOf(c, meta)); err != nil {
			return err
		}
		if c.AuthorID == p.UserID && !p.IsAdmin {
			return apperr.Forbidden("content.self_review", "You cannot review your own content.")
		}
		if ReviewState(c.ReviewState) != ReviewInReview {
			return invalidState("It is not waiting for review.")
		}
		rv, err := q.InsertReview(ctx, contentdb.InsertReviewParams{
			ContentID: c.ID, RevNo: c.DraftRevNo, ReviewerID: p.UserID, Decision: string(in.Decision), Summary: in.Summary,
		})
		if err != nil {
			return err
		}
		for _, n := range in.Notes {
			anchor, err := json.Marshal(n.Anchor)
			if err != nil {
				return apperr.ValidationFailed(map[string]string{"notes": "Note anchors must be JSON objects."})
			}
			if err := q.InsertReviewNote(ctx, contentdb.InsertReviewNoteParams{ReviewID: rv.ID, Anchor: anchor, Body: strings.TrimSpace(n.Body)}); err != nil {
				return err
			}
		}
		next := ReviewApproved
		if in.Decision == DecisionChangesRequested {
			next = ReviewChangesRequested
		}
		return s.setReview(ctx, tx, q, p.UserID, c, next, nil, "content.review")
	})
}

// Publish makes the draft live now. Editors may publish from any review state (an editor's
// own edits need no second reviewer); a never-reviewed draft by someone else is allowed too,
// because the editor is the one accountable for what goes live in their section.
func (s *Service) Publish(ctx context.Context, p auth.Principal, id int64) (Draft, error) {
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, body string) error {
		if err := s.policy.Require(ctx, p, access.RoleEditor, scopeOf(c, meta)); err != nil {
			return err
		}
		if Status(c.Status) == StatusPublished && c.PublishedRevNo != nil && *c.PublishedRevNo == c.DraftRevNo {
			return invalidState("The live version is already up to date.")
		}
		return s.publishLocked(ctx, tx, q, p.UserID, c, meta, body)
	})
}

// Schedule publishes an approved draft at a future time (the worker runs PublishDue).
func (s *Service) Schedule(ctx context.Context, p auth.Principal, id int64, in ScheduleInput) (Draft, error) {
	now := time.Now()
	if !in.At.After(now.Add(time.Minute)) || in.At.After(now.Add(maxScheduleAhead)) {
		return Draft{}, apperr.ValidationFailed(map[string]string{"at": "Schedule between one minute and one year from now."})
	}
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, body string) error {
		if err := s.policy.Require(ctx, p, access.RoleEditor, scopeOf(c, meta)); err != nil {
			return err
		}
		if rs := ReviewState(c.ReviewState); rs != ReviewApproved && rs != ReviewScheduled {
			return invalidState("Only approved drafts can be scheduled.")
		}
		if _, err := s.checkPublishable(ctx, tx, q, c, meta, body); err != nil {
			return err
		}
		return s.setReview(ctx, tx, q, p.UserID, c, ReviewScheduled, &in.At, "content.schedule")
	})
}

// Archive takes a live page down. Its history stays, and publishing again restores it.
func (s *Service) Archive(ctx context.Context, p auth.Principal, id int64) (Draft, error) {
	return s.transition(ctx, p, id, func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, _ string) error {
		if err := s.policy.Require(ctx, p, access.RoleEditor, scopeOf(c, meta)); err != nil {
			return err
		}
		if Status(c.Status) != StatusPublished {
			return invalidState("Only live content can be archived.")
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
		return s.recordLive(ctx, tx, p.UserID, c, "content.archive", outbox.TopicContentUnpublished, "")
	})
}

// Delete soft-deletes an item. Authors may delete their own never-published drafts; anything
// that has been live needs an editor.
func (s *Service) Delete(ctx context.Context, p auth.Principal, id int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		c, meta, err := s.lock(ctx, q, p, id)
		if err != nil {
			return err
		}
		ownDraft := c.AuthorID == p.UserID && Status(c.Status) == StatusDraft
		if !ownDraft {
			if err := s.policy.Require(ctx, p, access.RoleEditor, scopeOf(c, meta)); err != nil {
				return err
			}
		}
		if err := q.SoftDeleteContent(ctx, c.ID); err != nil {
			return err
		}
		if err := q.DeleteContentTags(ctx, c.ID); err != nil {
			return err
		}
		if Status(c.Status) == StatusPublished {
			if err := s.onUnpublish(ctx, tx, c); err != nil {
				return err
			}
			return s.recordLive(ctx, tx, p.UserID, c, "content.delete", outbox.TopicContentUnpublished, "")
		}
		return audit.Record(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "content.delete", Entity: "content", EntityID: c.ID})
	})
}

// PublishDue publishes scheduled items whose time has come. A draft that stopped being
// publishable since it was scheduled (say, an image was quarantined) drops back to APPROVED
// and is logged, rather than blocking the rest of the batch.
func (s *Service) PublishDue(ctx context.Context, batch int32) (int, error) {
	published := 0
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		ids, err := q.ClaimDueScheduled(ctx, batch)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := q.GetContentForUpdate(ctx, id)
			if err != nil {
				return err
			}
			meta, body, err := draftOf(ctx, q, c)
			if err != nil {
				return err
			}
			if err := s.publishLocked(ctx, tx, q, 0, c, meta, body); err != nil {
				if e, ok := apperr.As(err); ok && e.Kind != apperr.KindInternal {
					slog.Warn("scheduled publish failed", "content", id, "code", e.Code)
					if err := s.setReview(ctx, tx, q, 0, c, ReviewApproved, nil, "content.schedule_failed"); err != nil {
						return err
					}
					continue
				}
				return err
			}
			published++
		}
		return nil
	})
	return published, err
}

// RunScheduler publishes due items every interval until ctx ends.
func (s *Service) RunScheduler(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if n, err := s.PublishDue(ctx, 50); err != nil && ctx.Err() == nil {
			slog.Error("scheduled publishing failed", "err", err)
		} else if n > 0 {
			slog.Info("scheduled content published", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ── internals ────────────────────────────────────────────────────────────────

type transitionFn func(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, body string) error

// transition runs a workflow step on the locked row and returns the resulting studio view.
func (s *Service) transition(ctx context.Context, p auth.Principal, id int64, fn transitionFn) (Draft, error) {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := contentdb.New(tx)
		c, meta, err := s.lock(ctx, q, p, id)
		if err != nil {
			return err
		}
		_, body, err := draftOf(ctx, q, c)
		if err != nil {
			return err
		}
		return fn(ctx, tx, q, c, meta, body)
	})
	if err != nil {
		return Draft{}, err
	}
	return s.Get(ctx, p, id)
}

func (s *Service) requireAuthorOrEditor(ctx context.Context, p auth.Principal, c contentdb.Content, meta Meta) error {
	if c.AuthorID == p.UserID && !p.IsGuest {
		return nil
	}
	return s.policy.Require(ctx, p, access.RoleEditor, scopeOf(c, meta))
}

func (s *Service) setReview(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, actor int64, c contentdb.Content, state ReviewState, at *time.Time, action string) error {
	if err := q.SetReviewState(ctx, contentdb.SetReviewStateParams{ID: c.ID, ReviewState: string(state), ScheduledAt: at}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Entry{ActorID: actor, Action: action, Entity: "content", EntityID: c.ID,
		Data: map[string]any{"revNo": c.DraftRevNo, "from": c.ReviewState, "to": state}})
}

// checkPublishable runs every rule a live page must satisfy and reports all failures at once.
func (s *Service) checkPublishable(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, c contentdb.Content, meta Meta, body string) (*publishPlan, error) {
	a := markdown.Analyze([]byte(body))
	issues := append(metaIssues(Type(c.Type), meta), bodyIssues(a)...)

	assetIDs := a.AssetIDs
	if meta.Blog != nil && meta.Blog.CoverFileID != nil {
		assetIDs = append(assetIDs, *meta.Blog.CoverFileID)
	}
	files, err := s.assets.Describe(ctx, assetIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range assetIDs {
		if f, ok := files[id]; !ok || f.Purpose != asset.PurposeAsset {
			issues = append(issues, PublishIssue{Code: "asset.missing",
				Message: fmt.Sprintf("asset:%d is not an uploaded, verified content file.", id)})
		}
	}
	if meta.Blog != nil && meta.Blog.CoverFileID != nil {
		if f, ok := files[*meta.Blog.CoverFileID]; ok && !strings.HasPrefix(f.Mime, "image/") {
			issues = append(issues, PublishIssue{Field: "blog.coverFileId", Code: "meta.cover", Message: "The cover must be an image."})
		}
	}

	problems := []int64{}
	if len(a.ProblemSlugs) > 0 {
		rows, err := q.LiveIDsBySlug(ctx, contentdb.LiveIDsBySlugParams{Type: string(TypeProblem), Locale: c.Locale, Slugs: a.ProblemSlugs})
		if err != nil {
			return nil, err
		}
		found := map[string]int64{}
		for _, r := range rows {
			found[r.Slug] = r.ID
		}
		for _, slug := range a.ProblemSlugs {
			if id, ok := found[slug]; ok {
				problems = append(problems, id)
			} else {
				issues = append(issues, PublishIssue{Code: "problem.missing", Message: fmt.Sprintf("No published problem %q to embed.", slug)})
			}
		}
	}

	typeIssues, err := s.typeIssues(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	issues = append(issues, typeIssues...)

	if len(issues) > 0 {
		return nil, notPublishable(issues)
	}
	return &publishPlan{analysis: a, assetIDs: assetIDs, problemIDs: problems}, nil
}

type publishPlan struct {
	analysis   *markdown.Analysis
	assetIDs   []int64
	problemIDs []int64
}

// publishLocked makes the locked row's draft the live version: public face, render data,
// assets, tags, practice links, type fields, redirects, then the event that tells caches,
// search and sitemaps. actor 0 = the scheduler.
func (s *Service) publishLocked(ctx context.Context, tx pgx.Tx, q *contentdb.Queries, actor int64, c contentdb.Content, meta Meta, body string) error {
	plan, err := s.checkPublishable(ctx, tx, q, c, meta, body)
	if err != nil {
		return err
	}
	wasLive := Status(c.Status) == StatusPublished
	oldSlug := ""
	if c.PublishedRevNo != nil && c.Slug != meta.Slug {
		// Anything ever published under the old slug may be linked or indexed: keep it working.
		oldSlug = c.Slug
		if err := q.UpsertSlugRedirect(ctx, contentdb.UpsertSlugRedirectParams{Type: c.Type, Locale: c.Locale, OldSlug: oldSlug, ContentID: c.ID}); err != nil {
			return err
		}
	}
	if err := q.DeleteSlugRedirect(ctx, contentdb.DeleteSlugRedirectParams{Type: c.Type, Locale: c.Locale, Slug: meta.Slug}); err != nil {
		return err
	}

	live, err := q.PublishContent(ctx, contentdb.PublishContentParams{
		ID: c.ID, Slug: meta.Slug, Title: meta.Title, Summary: meta.Summary, PrimaryCategoryID: meta.CategoryID,
		Visibility: meta.Visibility, Access: meta.Access, RevNo: c.DraftRevNo,
	})
	if err != nil {
		return mapContentErr(err)
	}

	a := plan.analysis
	toc, _ := json.Marshal(a.TOC)
	if err := q.UpsertRender(ctx, contentdb.UpsertRenderParams{
		ContentID: c.ID, RevNo: c.DraftRevNo, Toc: toc, PlainText: a.PlainText, WordCount: int32(a.WordCount),
		ReadingMinutes: int32(a.ReadingMinutes), SnippetCount: int32(len(a.Snippets)),
	}); err != nil {
		return err
	}
	if err := q.DeleteContentAssets(ctx, c.ID); err != nil {
		return err
	}
	if err := q.InsertContentAssets(ctx, contentdb.InsertContentAssetsParams{ContentID: c.ID, FileIds: plan.assetIDs}); err != nil {
		return err
	}
	// Only PUBLIC items appear in tag listings; UNLISTED pages keep their tags but stay out.
	var listedAt *time.Time
	if meta.Visibility == VisibilityPublic {
		listedAt = live.PublishedAt
	}
	if err := replaceTags(ctx, q, c.ID, meta.TagIDs, listedAt); err != nil {
		return err
	}
	if err := q.DeleteLinksOfKind(ctx, contentdb.DeleteLinksOfKindParams{FromID: c.ID, Kind: "PRACTICE"}); err != nil {
		return err
	}
	if err := q.InsertLinks(ctx, contentdb.InsertLinksParams{FromID: c.ID, Kind: "PRACTICE", ToIds: plan.problemIDs}); err != nil {
		return err
	}
	related, err := relatedIDs(ctx, q, c.ID, Type(c.Type), meta.TagIDs)
	if err != nil {
		return err
	}
	if err := q.DeleteLinksOfKind(ctx, contentdb.DeleteLinksOfKindParams{FromID: c.ID, Kind: "RELATED"}); err != nil {
		return err
	}
	if err := q.InsertLinks(ctx, contentdb.InsertLinksParams{FromID: c.ID, Kind: "RELATED", ToIds: related}); err != nil {
		return err
	}
	if err := writeTypeFields(ctx, q, c.ID, meta); err != nil {
		return err
	}
	if err := s.onPublish(ctx, tx, live, meta.TagIDs); err != nil {
		return err
	}

	topic := outbox.TopicContentPublished
	if wasLive {
		topic = outbox.TopicContentUpdated
	}
	return s.recordLive(ctx, tx, actor, live, "content.publish", topic, oldSlug)
}

func writeTypeFields(ctx context.Context, q *contentdb.Queries, id int64, m Meta) error {
	switch {
	case m.Article != nil:
		return q.UpsertArticleExt(ctx, contentdb.UpsertArticleExtParams{ContentID: id, Level: m.Article.Level})
	case m.Blog != nil:
		return q.UpsertBlogExt(ctx, contentdb.UpsertBlogExtParams{ContentID: id, CoverFileID: m.Blog.CoverFileID})
	}
	return nil
}

// recordLive audits a change to what readers see and emits the event that refreshes caches,
// search and sitemaps.
func (s *Service) recordLive(ctx context.Context, tx pgx.Tx, actor int64, c contentdb.Content, action, topic, oldSlug string) error {
	if err := audit.Record(ctx, tx, audit.Entry{ActorID: actor, Action: action, Entity: "content", EntityID: c.ID,
		Data: map[string]any{"revNo": c.PublishedRevNo, "slug": c.Slug}}); err != nil {
		return err
	}
	return outbox.Publish(ctx, tx, topic, c.ID, outbox.ContentEvent{Type: c.Type, Locale: c.Locale, Slug: c.Slug, OldSlug: oldSlug})
}

// Reviews lists the decisions on an item (newest first) with their notes.
func (s *Service) Reviews(ctx context.Context, p auth.Principal, id int64) ([]Review, error) {
	q := contentdb.New(s.db.Primary)
	if _, err := s.viewable(ctx, q, p, id); err != nil {
		return nil, err
	}
	rows, err := q.ListReviews(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]Review, len(rows))
	ids := make([]int64, len(rows))
	byID := make(map[int64]*Review, len(rows))
	for i, r := range rows {
		out[i] = Review{ID: r.ID, RevNo: r.RevNo, ReviewerID: r.ReviewerID, Decision: ReviewDecision(r.Decision),
			Summary: r.Summary, Notes: []ReviewNote{}, CreatedAt: r.CreatedAt}
		ids[i] = r.ID
		byID[r.ID] = &out[i]
	}
	notes, err := q.ListReviewNotes(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		var anchor map[string]any
		_ = json.Unmarshal(n.Anchor, &anchor)
		byID[n.ReviewID].Notes = append(byID[n.ReviewID].Notes, ReviewNote{Anchor: anchor, Body: n.Body})
	}
	return out, nil
}

const (
	relatedPerTagSample = 200
	maxRelated          = 5
)

// relatedIDs picks up to five live items of the same type that share the most tags. Each tag
// contributes only its most recent members, so the cost stays bounded for huge tags; ties go
// to the newer item. Computed at publish (not per page view) and stored as RELATED links.
func relatedIDs(ctx context.Context, q *contentdb.Queries, id int64, t Type, tagIDs []int64) ([]int64, error) {
	overlap := map[int64]int{}
	for _, tag := range tagIDs {
		members, err := q.RecentInTag(ctx, contentdb.RecentInTagParams{TagID: tag, RowLimit: relatedPerTagSample})
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if m != id {
				overlap[m]++
			}
		}
	}
	if len(overlap) == 0 {
		return []int64{}, nil
	}
	candidates := make([]int64, 0, len(overlap))
	for cid := range overlap {
		candidates = append(candidates, cid)
	}
	live, err := q.ListLiveByIDs(ctx, candidates)
	if err != nil {
		return nil, err
	}
	same := make([]int64, 0, len(live))
	for _, c := range live {
		if Type(c.Type) == t {
			same = append(same, c.ID)
		}
	}
	slices.SortFunc(same, func(a, b int64) int {
		if d := overlap[b] - overlap[a]; d != 0 {
			return d
		}
		return cmp.Compare(b, a)
	})
	if len(same) > maxRelated {
		same = same[:maxRelated]
	}
	return same, nil
}
