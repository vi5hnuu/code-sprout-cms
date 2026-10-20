package content

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/taxonomy"
)

// fixture is one isolated section of the site with the people who work in it.
type fixture struct {
	t        *testing.T
	d        *db.DB
	svc      *Service
	tax      *taxonomy.Service
	category taxonomy.Category
	author   auth.Principal
	reviewer auth.Principal // REVIEWER scoped to category
	editor   auth.Principal // EDITOR scoped to category
	outsider auth.Principal // signed in, no grants
	tagID    int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	d := testenv.DB(t)
	policy := access.NewPolicy(d.Primary)
	tax := taxonomy.NewService(d, policy)
	// Describe only reads the file table, so no storage client is needed here.
	assets := asset.NewService(d, nil, asset.Config{PublicBucket: "sprout-public", PublicBaseURL: "https://cdn.test"})
	f := &fixture{t: t, d: d, svc: NewService(d, policy, assets, tax), tax: tax}

	admin := testenv.Admin(t, d)
	cat, err := tax.Create(context.Background(), admin, taxonomy.CreateCategory{Slug: testenv.Unique("sec"), Name: "Section"})
	if err != nil {
		t.Fatal(err)
	}
	f.category = cat
	tag, err := tax.CreateTag(context.Background(), admin, taxonomy.CreateTag{Kind: taxonomy.TagTopic, Slug: testenv.Unique("tag"), Name: "Arrays"})
	if err != nil {
		t.Fatal(err)
	}
	f.tagID = tag.ID
	f.author = testenv.User(t, d)
	f.reviewer = testenv.User(t, d)
	testenv.Grant(t, d, f.reviewer, "REVIEWER", &cat.ID)
	f.editor = testenv.User(t, d)
	testenv.Grant(t, d, f.editor, "EDITOR", &cat.ID)
	f.outsider = testenv.User(t, d)
	return f
}

// readyAsset inserts a verified public image as if it had been uploaded.
func (f *fixture) readyAsset() int64 {
	f.t.Helper()
	sum := sha256.Sum256([]byte(testenv.Unique("img")))
	var id int64
	err := f.d.Primary.QueryRow(context.Background(), `
		INSERT INTO file (owner_id, bucket, storage_key, mime, size_bytes, sha256, status, purpose, width, height)
		VALUES ($1, 'sprout-public', $2, 'image/png', 10, $3, 'READY', 'ASSET', 4, 3) RETURNING id`,
		f.author.UserID, testenv.Unique("k"), sum[:]).Scan(&id)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) publishableDraft(slug string, imageID int64) DraftInput {
	summary := "A complete walkthrough of binary search with runnable examples in two languages."
	body := fmt.Sprintf("## Idea\n\nHalve the range.\n\n![Diagram](asset:%d)\n\n```python runnable\nprint(1)\n```\n", imageID)
	return DraftInput{
		Meta: Meta{Title: "Binary Search", Slug: slug, Summary: &summary, CategoryID: &f.category.ID, TagIDs: []int64{f.tagID}},
		Body: body,
	}
}

func wantErr(t *testing.T, err error, kind apperr.Kind, code string) *apperr.Error {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Kind != kind || (code != "" && e.Code != code) {
		t.Fatalf("err = %v, want kind %d code %q", err, kind, code)
	}
	return e
}

func (f *fixture) count(query string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.d.Primary.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestDraftEditing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// The test database outlives runs, so the title carries a unique token.
	token := testenv.Unique("run")
	d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: DraftInput{Meta: Meta{Title: "Ünïcödé Two-Pointer Tricks! " + token}}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Meta.Slug != "unicode-two-pointer-tricks-"+token || d.DraftRevNo != 1 || d.Status != StatusDraft || d.Locale != "en" {
		t.Fatalf("created = %+v", d)
	}

	in := DraftInput{Meta: d.Meta, Body: "## Hello\n"}
	_, err = f.svc.SaveDraft(ctx, f.author, d.ID, 0, in)
	e := wantErr(t, err, apperr.KindPreconditionFailed, "content.stale_revision")
	if e.Details.(map[string]any)["currentRevNo"] != int32(1) {
		t.Fatalf("details = %v", e.Details)
	}

	d2, err := f.svc.SaveDraft(ctx, f.author, d.ID, 1, in)
	if err != nil || d2.DraftRevNo != 2 {
		t.Fatalf("save = %+v, %v", d2.DraftRevNo, err)
	}
	same, err := f.svc.SaveDraft(ctx, f.author, d.ID, 2, in)
	if err != nil || same.DraftRevNo != 2 {
		t.Fatalf("no-op save created a revision: %d, %v", same.DraftRevNo, err)
	}

	// Never-published drafts mirror their title, so studio lists show it.
	in.Title = "Renamed draft"
	if _, err := f.svc.SaveDraft(ctx, f.author, d.ID, 2, in); err != nil {
		t.Fatal(err)
	}
	if n := f.count("SELECT count(*) FROM content WHERE id=$1 AND title='Renamed draft'", d.ID); n != 1 {
		t.Fatal("draft title not mirrored onto the content row")
	}

	other, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeBlog, DraftInput: DraftInput{Meta: Meta{Title: "Other", Slug: testenv.Unique("taken")}}})
	if err != nil {
		t.Fatal(err)
	}
	clash := DraftInput{Meta: other.Meta}
	clash.Slug = d.Meta.Slug
	_, err = f.svc.SaveDraft(ctx, f.author, other.ID, other.DraftRevNo, clash)
	if err != nil {
		t.Fatalf("slugs are unique per type; a BLOG may reuse an ARTICLE slug: %v", err)
	}
	_, err = f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: DraftInput{Meta: Meta{Title: "Dup", Slug: d.Meta.Slug}}})
	wantErr(t, err, apperr.KindConflict, "content.slug_taken")

	bad := DraftInput{Meta: Meta{Title: "x", TagIDs: []int64{-5}}}
	_, err = f.svc.SaveDraft(ctx, f.author, d.ID, 3, bad)
	if e := wantErr(t, err, apperr.KindInvalid, "validation_failed"); e.Fields["tagIds"] == "" {
		t.Fatalf("fields = %v", e.Fields)
	}
	_, err = f.svc.Create(ctx, f.author, CreateInput{Type: TypeProblem, DraftInput: DraftInput{Meta: Meta{Title: "p"}}})
	wantErr(t, err, apperr.KindInvalid, "validation_failed")

	// Drafts are private to their author and the section's reviewers/editors.
	_, err = f.svc.Get(ctx, f.outsider, d.ID)
	wantErr(t, err, apperr.KindNotFound, "content.not_found")
	if _, err := f.svc.Get(ctx, f.reviewer, d.ID); err == nil {
		t.Fatal("reviewer saw a draft with no category in their scope")
	}
	_, err = f.svc.SaveDraft(ctx, f.outsider, d.ID, 3, in)
	wantErr(t, err, apperr.KindNotFound, "")
}

func TestConcurrentSavesNeverLoseWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: DraftInput{Meta: Meta{Title: "Race", Slug: testenv.Unique("race")}}})
	if err != nil {
		t.Fatal(err)
	}
	const writers = 8
	var wg sync.WaitGroup
	results := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			in := DraftInput{Meta: d.Meta, Body: fmt.Sprintf("## Writer %d\n", i)}
			_, results[i] = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, in)
		})
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, err := range results {
		if err == nil {
			ok++
		} else if e, isApp := apperr.As(err); isApp && e.Code == "content.stale_revision" {
			stale++
		} else {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if ok != 1 || stale != writers-1 {
		t.Fatalf("ok=%d stale=%d; exactly one save from the same revision may win", ok, stale)
	}
}

func TestEditorialWorkflow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	img := f.readyAsset()
	slug := testenv.Unique("binary-search")

	d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: DraftInput{
		Meta: Meta{Title: "Binary Search", Slug: slug}, Body: "# Wrong heading\n\n<b>html</b>\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Issues) == 0 {
		t.Fatal("studio view should surface Markdown issues")
	}

	// Submitting reports every blocker at once: Markdown, summary and category.
	_, err = f.svc.Submit(ctx, f.author, d.ID)
	e := wantErr(t, err, apperr.KindInvalid, "content.not_publishable")
	codes := fmt.Sprint(e.Details)
	for _, c := range []string{"heading.h1", "html.not_allowed", "meta.summary", "meta.category"} {
		if !strings.Contains(codes, c) {
			t.Fatalf("missing %s in %s", c, codes)
		}
	}

	d, err = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, f.publishableDraft(slug, img))
	if err != nil {
		t.Fatal(err)
	}
	if d, err = f.svc.Submit(ctx, f.author, d.ID); err != nil || d.ReviewState != ReviewInReview {
		t.Fatalf("submit = %v, %v", d.ReviewState, err)
	}
	// What is under review is frozen for the author.
	_, err = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, f.publishableDraft(slug, img))
	wantErr(t, err, apperr.KindConflict, "content.in_review")

	// The reviewer now sees it (its category is in scope) and asks for changes.
	queue, err := f.svc.ReviewQueue(ctx, f.reviewer, ReviewInReview, page.Request[ListCursor]{Limit: 50})
	if err != nil || !containsID(queue.Items, d.ID) {
		t.Fatalf("review queue missing item: %v", err)
	}
	if q, _ := f.svc.ReviewQueue(ctx, f.outsider, ReviewInReview, page.Request[ListCursor]{Limit: 50}); containsID(q.Items, d.ID) {
		t.Fatal("outsider sees the review queue item")
	}
	note := ReviewNote{Anchor: map[string]any{"line": 3}, Body: "Explain why the range halves."}
	d, err = f.svc.Review(ctx, f.reviewer, d.ID, ReviewInput{Decision: DecisionChangesRequested, Notes: []ReviewNote{note}})
	if err != nil || d.ReviewState != ReviewChangesRequested {
		t.Fatalf("request changes = %v, %v", d.ReviewState, err)
	}

	in := f.publishableDraft(slug, img)
	in.Body += "\nEach step discards half of the remaining range.\n"
	if d, err = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, in); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Submit(ctx, f.author, d.ID); err != nil {
		t.Fatal(err)
	}
	testenv.Grant(t, f.d, f.author, "REVIEWER", &f.category.ID)
	_, err = f.svc.Review(ctx, f.author, d.ID, ReviewInput{Decision: DecisionApproved})
	wantErr(t, err, apperr.KindForbidden, "content.self_review")
	if d, err = f.svc.Review(ctx, f.reviewer, d.ID, ReviewInput{Decision: DecisionApproved}); err != nil || d.ReviewState != ReviewApproved {
		t.Fatalf("approve = %v, %v", d.ReviewState, err)
	}
	reviews, err := f.svc.Reviews(ctx, f.author, d.ID)
	if err != nil || len(reviews) != 2 || len(reviews[1].Notes) != 1 {
		t.Fatalf("reviews = %+v, %v", reviews, err)
	}

	_, err = f.svc.Publish(ctx, f.reviewer, d.ID)
	wantErr(t, err, apperr.KindForbidden, "")
	d, err = f.svc.Publish(ctx, f.editor, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusPublished || d.ReviewState != ReviewNone || d.Live == nil || d.HasUnpublishedChanges || d.PublishedAt == nil {
		t.Fatalf("published = %+v", d)
	}
	if f.count("SELECT count(*) FROM content_render WHERE content_id=$1 AND snippet_count=1 AND word_count>0", d.ID) != 1 ||
		f.count("SELECT count(*) FROM content_asset WHERE content_id=$1 AND file_id=$2", d.ID, img) != 1 ||
		f.count("SELECT count(*) FROM content_tag WHERE content_id=$1 AND published_at IS NOT NULL", d.ID) != 1 ||
		f.count("SELECT count(*) FROM article_ext WHERE content_id=$1", d.ID) != 1 ||
		f.count("SELECT count(*) FROM outbox WHERE topic='content.published' AND aggregate_id=$1", d.ID) != 1 {
		t.Fatal("publish did not write its derived data and event")
	}

	// Editing a live page changes nothing readers see until it is published again.
	firstLiveRev := d.Live.RevNo
	renamed := f.publishableDraft(slug+"-explained", img)
	renamed.Title = "Binary Search, Explained"
	if d, err = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, renamed); err != nil {
		t.Fatal(err)
	}
	if d.Live.Slug != slug || d.Live.Title != "Binary Search" || !d.HasUnpublishedChanges || d.Live.RevNo != firstLiveRev {
		t.Fatalf("live face changed before publish: %+v", d.Live)
	}
	if d, err = f.svc.Publish(ctx, f.editor, d.ID); err != nil {
		t.Fatal(err)
	}
	if d.Live.Slug != slug+"-explained" {
		t.Fatalf("live slug = %s", d.Live.Slug)
	}
	if f.count("SELECT count(*) FROM slug_redirect WHERE old_slug=$1 AND content_id=$2", slug, d.ID) != 1 ||
		f.count("SELECT count(*) FROM outbox WHERE topic='content.updated' AND aggregate_id=$1 AND payload->>'oldSlug'=$2", d.ID, slug) != 1 {
		t.Fatal("slug change did not leave a redirect and an update event with the old slug")
	}
	_, err = f.svc.Publish(ctx, f.editor, d.ID)
	wantErr(t, err, apperr.KindConflict, "content.invalid_state")

	// Archive takes it down; publishing again restores it.
	if d, err = f.svc.Archive(ctx, f.editor, d.ID); err != nil || d.Status != StatusArchived {
		t.Fatalf("archive = %v, %v", d.Status, err)
	}
	if f.count("SELECT count(*) FROM content_tag WHERE content_id=$1 AND published_at IS NOT NULL", d.ID) != 0 {
		t.Fatal("archived item still in tag listings")
	}
	if d, err = f.svc.Publish(ctx, f.editor, d.ID); err != nil || d.Status != StatusPublished {
		t.Fatalf("republish = %v, %v", d.Status, err)
	}

	// Only editors delete what has been live.
	err = f.svc.Delete(ctx, f.author, d.ID)
	wantErr(t, err, apperr.KindForbidden, "")
	if err := f.svc.Delete(ctx, f.editor, d.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Get(ctx, f.editor, d.ID)
	wantErr(t, err, apperr.KindNotFound, "")
}

func TestPublishRequiresVerifiedAssetsAndLiveProblems(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	slug := testenv.Unique("assets")
	in := f.publishableDraft(slug, 987654321)
	in.Body += "\n::problem{slug=no-such-problem}\n"
	d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: in})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.Publish(ctx, f.editor, d.ID)
	e := wantErr(t, err, apperr.KindInvalid, "content.not_publishable")
	if s := fmt.Sprint(e.Details); !strings.Contains(s, "asset.missing") || !strings.Contains(s, "problem.missing") {
		t.Fatalf("issues = %s", s)
	}
}

func TestScheduledPublishing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	img := f.readyAsset()
	approve := func(slug string) Draft {
		d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: f.publishableDraft(slug, img)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Submit(ctx, f.author, d.ID); err != nil {
			t.Fatal(err)
		}
		if d, err = f.svc.Review(ctx, f.reviewer, d.ID, ReviewInput{Decision: DecisionApproved}); err != nil {
			t.Fatal(err)
		}
		return d
	}
	good, broken, clash := approve(testenv.Unique("sched")), approve(testenv.Unique("sched")), approve(testenv.Unique("sched"))

	_, err := f.svc.Schedule(ctx, f.editor, good.ID, ScheduleInput{At: time.Now().Add(-time.Hour)})
	wantErr(t, err, apperr.KindInvalid, "validation_failed")
	for _, d := range []Draft{clash, good, broken} {
		s, err := f.svc.Schedule(ctx, f.editor, d.ID, ScheduleInput{At: time.Now().Add(10 * time.Minute)})
		if err != nil || s.ReviewState != ReviewScheduled || s.ScheduledAt == nil {
			t.Fatalf("schedule = %+v, %v", s, err)
		}
	}

	// Time passes; meanwhile the broken item's image fails verification.
	if _, err := f.d.Primary.Exec(ctx, "UPDATE content SET scheduled_at = now() - interval '1 second' WHERE id = ANY($1)", []int64{good.ID, broken.ID}); err != nil {
		t.Fatal(err)
	}
	// The clash comes due first and fails in the database itself (its slug is now taken), which
	// must not roll back or block the items after it.
	if _, err := f.d.Primary.Exec(ctx, "UPDATE content SET scheduled_at = now() - interval '1 minute' WHERE id = $1", clash.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Primary.Exec(ctx, "UPDATE content_revision SET meta = jsonb_set(meta, '{slug}', to_jsonb($1::text)) WHERE content_id=$2 AND rev_no=$3",
		good.Meta.Slug, clash.ID, clash.DraftRevNo); err != nil {
		t.Fatal(err)
	}
	brokenImg := f.readyAsset()
	brokenBody := f.publishableDraft(broken.Meta.Slug, brokenImg)
	if _, err := f.d.Primary.Exec(ctx, "UPDATE content_revision SET body_md=$1 WHERE content_id=$2 AND rev_no=$3", brokenBody.Body, broken.ID, broken.DraftRevNo); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Primary.Exec(ctx, "UPDATE file SET status='QUARANTINED' WHERE id=$1", brokenImg); err != nil {
		t.Fatal(err)
	}

	for {
		n, err := f.svc.PublishDue(ctx, 50)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if g, _ := f.svc.Get(ctx, f.editor, good.ID); g.Status != StatusPublished || g.ScheduledAt != nil {
		t.Fatalf("scheduled item not published: %+v", g)
	}
	for _, d := range []Draft{broken, clash} {
		if b, _ := f.svc.Get(ctx, f.editor, d.ID); b.Status != StatusDraft || b.ReviewState != ReviewApproved {
			t.Fatalf("failed item %d should fall back to APPROVED: %s/%s", d.ID, b.Status, b.ReviewState)
		}
	}
	if f.count("SELECT count(*) FROM audit_log WHERE entity_id=$1 AND action='content.schedule_failed' AND actor_id IS NULL", broken.ID) != 1 {
		t.Fatal("failed scheduled publish was not audited as a system action")
	}
}

func TestRevisionsAndRestore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	d, err := f.svc.Create(ctx, f.author, CreateInput{Type: TypeArticle, DraftInput: DraftInput{Meta: Meta{Title: "History", Slug: testenv.Unique("hist")}, Body: "## v1\n"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 4; i++ {
		if d, err = f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, DraftInput{Meta: d.Meta, Body: fmt.Sprintf("## v%d\n", i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.svc.Revisions(ctx, f.author, d.ID, page.Request[int32]{Limit: 3})
	if err != nil || len(first.Items) != 3 || first.Items[0].RevNo != 4 || first.NextCursor == nil {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	last := first.Items[len(first.Items)-1].RevNo
	second, err := f.svc.Revisions(ctx, f.author, d.ID, page.Request[int32]{Limit: 3, Key: &last})
	if err != nil || len(second.Items) != 1 || second.Items[0].RevNo != 1 {
		t.Fatalf("page 2 = %+v, %v", second, err)
	}

	d, err = f.svc.Restore(ctx, f.author, d.ID, 1, d.DraftRevNo)
	if err != nil || d.DraftRevNo != 5 || d.Body != "## v1\n" {
		t.Fatalf("restore = rev %d body %q, %v", d.DraftRevNo, d.Body, err)
	}
	r, err := f.svc.RevisionDetail(ctx, f.author, d.ID, 5)
	if err != nil || r.Message == nil || *r.Message != "Restored revision 1" {
		t.Fatalf("restored revision = %+v, %v", r, err)
	}
	if err := f.svc.Delete(ctx, f.author, d.ID); err != nil {
		t.Fatalf("author deletes own draft: %v", err)
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Binary Search":           "binary-search",
		"  C++ & Java: Tips!  ":   "c-java-tips",
		"Ünïcödé Tïtlé":           "unicode-title",
		"O(n log n) sorting":      "o-n-log-n-sorting",
		"द्विआधारी खोज":           "",
		strings.Repeat("ab ", 80): strings.TrimSuffix(strings.Repeat("ab-", 40), "-"),
	} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func containsID(items []Summary, id int64) bool {
	for _, s := range items {
		if s.ID == id {
			return true
		}
	}
	return false
}
