package collection_test

import (
	"context"
	"strings"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/library"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
)

func publishTwo(t *testing.T, e *arenatest.Env) (int64, int64) {
	var ids [2]int64
	for i := range ids {
		tag := testenv.Unique("c")
		ids[i] = e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)
	}
	return ids[0], ids[1]
}

func TestReaderListsAndEditorialCollections(t *testing.T) {
	e := arenatest.New(t)
	svc := collection.NewService(e.DB, e.Reader, access.NewPolicy(e.DB.Primary))
	ctx := context.Background()
	a, b := publishTwo(t, e)
	reader := testenv.User(t, e.DB)

	l, err := svc.Create(ctx, reader, collection.Input{Title: "Revisit later"})
	if err != nil || l.Kind != collection.KindList || l.Visibility != "PRIVATE" {
		t.Fatalf("create = %+v, %v", l, err)
	}
	for _, id := range []int64{a, b, a} { // re-adding is a no-op
		if err := svc.AddItem(ctx, reader, l.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.AddItem(ctx, reader, l.ID, 999999999); err == nil {
		t.Fatal("non-live item added")
	}
	v, err := svc.View(ctx, &reader, l.ID, page.Request[float64]{Limit: 10})
	if err != nil || len(v.Items) != 2 || v.Items[0].ID != a {
		t.Fatalf("view = %+v, %v", v.Items, err)
	}
	// The total counts every item, not just the page shown.
	if v, err := svc.View(ctx, &reader, l.ID, page.Request[float64]{Limit: 1}); err != nil || len(v.Items) != 1 || v.ItemCount == nil || *v.ItemCount != 2 {
		t.Fatalf("one-item page = %d items, count %v, %v", len(v.Items), v.ItemCount, err)
	}
	if err := svc.Reorder(ctx, reader, l.ID, []int64{b, a}); err != nil {
		t.Fatal(err)
	}
	if v, _ = svc.View(ctx, &reader, l.ID, page.Request[float64]{Limit: 10}); v.Items[0].ID != b {
		t.Fatalf("order after reorder = %v", v.Items)
	}
	if err := svc.Reorder(ctx, reader, l.ID, []int64{b}); err == nil {
		t.Fatal("reorder dropping an item accepted")
	}

	// Private lists are invisible to others; editing someone else's list looks like a 404.
	other := testenv.User(t, e.DB)
	if _, err := svc.View(ctx, &other, l.ID, page.Request[float64]{Limit: 10}); err == nil {
		t.Fatal("private list visible to another reader")
	}
	if err := svc.AddItem(ctx, other, l.ID, a); err == nil {
		t.Fatal("another reader edited the list")
	}
	containing, err := svc.Containing(ctx, reader, a)
	if err != nil || len(containing) != 1 || containing[0] != l.ID {
		t.Fatalf("containing = %v, %v", containing, err)
	}

	// Editorial collections need a site-wide editor and are served by slug when public.
	_, err = svc.Create(ctx, reader, collection.Input{Kind: collection.KindStudyPlan, Title: "Top 150"})
	if ae, ok := apperr.As(err); !ok || ae.Kind != apperr.KindForbidden {
		t.Fatalf("reader created a study plan: %v", err)
	}
	editor := testenv.User(t, e.DB)
	testenv.Grant(t, e.DB, editor, "EDITOR", nil)
	slug := testenv.Unique("top")
	plan, err := svc.Create(ctx, editor, collection.Input{Kind: collection.KindStudyPlan, Slug: slug, Title: "Top 150", Visibility: "PUBLIC"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AddItem(ctx, editor, plan.ID, a); err != nil {
		t.Fatal(err)
	}
	pub, err := svc.PublicBySlug(ctx, collection.KindStudyPlan, slug, page.Request[float64]{Limit: 10})
	if err != nil || len(pub.Items) != 1 {
		t.Fatalf("public plan = %+v, %v", pub, err)
	}
}

func TestNotes(t *testing.T) {
	e := arenatest.New(t)
	notes := library.NewNotes(e.DB, e.Reader)
	ctx := context.Background()
	a, _ := publishTwo(t, e)
	u := testenv.User(t, e.DB)

	n, err := notes.Put(ctx, u, a, "Two pointers from both ends.")
	if err != nil || n == nil || n.Body != "Two pointers from both ends." {
		t.Fatalf("put = %+v, %v", n, err)
	}
	if got, err := notes.Get(ctx, u, a); err != nil || got.Body != n.Body {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := notes.Get(ctx, testenv.User(t, e.DB), a); err == nil {
		t.Fatal("another reader read the note")
	}
	if n, err = notes.Put(ctx, u, a, "   "); err != nil || n != nil {
		t.Fatalf("empty body should delete: %+v, %v", n, err)
	}
	if _, err := notes.Get(ctx, u, a); err == nil {
		t.Fatal("note survived deletion")
	}
}

// Mirrored collections (YouTube playlists) are listed by source; other series stay out of that list.
func TestPublicEditorialBySource(t *testing.T) {
	e := arenatest.New(t)
	svc := collection.NewService(e.DB, e.Reader, access.NewPolicy(e.DB.Primary))
	ctx := context.Background()
	a, _ := publishTwo(t, e)
	owner := testenv.User(t, e.DB)
	editor := testenv.User(t, e.DB)
	testenv.Grant(t, e.DB, editor, "EDITOR", nil)
	plain, err := svc.Create(ctx, editor, collection.Input{Kind: collection.KindSeries, Slug: testenv.Unique("plain"), Title: "Plain", Visibility: "PUBLIC"})
	if err != nil {
		t.Fatal(err)
	}

	// A source unique to this run keeps parallel tests and earlier runs out of the result.
	source := "T" + strings.ToUpper(strings.ReplaceAll(testenv.Unique("src"), "-", "_"))
	tx, err := e.DB.Primary.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slug := testenv.Unique("pl")
	id, err := svc.UpsertMirrored(ctx, tx, owner.UserID, collection.Mirrored{
		Source: source, ExternalRef: "PL1", Kind: collection.KindSeries, Slug: slug, FallbackSlug: slug + "-2", Title: "Playlist", ContentIDs: []int64{a},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := svc.PublicEditorial(ctx, collection.KindSeries, source, page.Request[collection.EditorialCursor]{Limit: 10})
	if err != nil || len(got.Items) != 1 || got.Items[0].ID != id || got.Items[0].Source == nil || *got.Items[0].Source != source {
		t.Fatalf("by source = %+v, %v", got.Items, err)
	}
	if got.Items[0].ItemCount == nil || *got.Items[0].ItemCount != 1 {
		t.Fatalf("item count = %v", got.Items[0].ItemCount)
	}
	all, err := svc.PublicEditorial(ctx, collection.KindSeries, "", page.Request[collection.EditorialCursor]{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, c := range all.Items {
		seen[c.ID] = true
	}
	if !seen[id] || !seen[plain.ID] {
		t.Fatalf("unfiltered listing missing %d or %d", id, plain.ID)
	}
}
