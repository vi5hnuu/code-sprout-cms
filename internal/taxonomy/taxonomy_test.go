package taxonomy

import (
	"context"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
)

func wantKind(t *testing.T, err error, kind apperr.Kind, code string) {
	t.Helper()
	e, ok := apperr.As(err)
	if !ok || e.Kind != kind || (code != "" && e.Code != code) {
		t.Fatalf("err = %v, want kind %d code %q", err, kind, code)
	}
}

func TestCategoryTreeLifecycle(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d, access.NewPolicy(d.Primary))
	ctx := context.Background()
	admin := testenv.Admin(t, d)

	root, err := svc.Create(ctx, admin, CreateCategory{Slug: testenv.Unique("dsa"), Name: "DSA"})
	if err != nil {
		t.Fatal(err)
	}
	algo, err := svc.Create(ctx, admin, CreateCategory{ParentID: &root.ID, Slug: "algorithms", Name: "Algorithms"})
	if err != nil {
		t.Fatal(err)
	}
	search, err := svc.Create(ctx, admin, CreateCategory{ParentID: &algo.ID, Slug: "searching", Name: "Searching"})
	if err != nil {
		t.Fatal(err)
	}
	if search.Path != root.Slug+"/algorithms/searching" {
		t.Fatalf("path = %q", search.Path)
	}
	sorting, err := svc.Create(ctx, admin, CreateCategory{ParentID: &algo.ID, Slug: "sorting", Name: "Sorting"})
	if err != nil {
		t.Fatal(err)
	}
	if sorting.Position <= search.Position {
		t.Fatalf("appended sibling should sort last: %v vs %v", sorting.Position, search.Position)
	}

	_, err = svc.Create(ctx, admin, CreateCategory{ParentID: &algo.ID, Slug: "searching", Name: "Dup"})
	wantKind(t, err, apperr.KindConflict, "category.slug_taken")

	view, err := svc.ByPath(ctx, search.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Ancestors) != 2 || view.Ancestors[0].ID != root.ID || view.Ancestors[1].ID != algo.ID {
		t.Fatalf("breadcrumb = %+v", view.Ancestors)
	}
	view, _ = svc.ByPath(ctx, algo.Path)
	if len(view.Children) != 2 || view.Children[0].Slug != "searching" {
		t.Fatalf("children = %+v", view.Children)
	}

	// Moving a subtree rewrites every descendant's path.
	other, err := svc.Create(ctx, admin, CreateCategory{Slug: testenv.Unique("cs"), Name: "CS"})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := svc.Move(ctx, admin, algo.ID, MoveCategory{ParentID: &other.ID, Slug: "algos"})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Path != other.Slug+"/algos" {
		t.Fatalf("moved path = %q", moved.Path)
	}
	child, err := svc.Get(ctx, search.ID)
	if err != nil || child.Path != other.Slug+"/algos/searching" {
		t.Fatalf("descendant path = %q (%v)", child.Path, err)
	}
	if _, err := svc.ByPath(ctx, search.Path); err == nil {
		t.Fatal("old path still resolves")
	}

	_, err = svc.Move(ctx, admin, other.ID, MoveCategory{ParentID: &search.ID, Slug: "loop"})
	wantKind(t, err, apperr.KindInvalid, "category.move_into_self")

	err = svc.Delete(ctx, admin, moved.ID)
	wantKind(t, err, apperr.KindConflict, "category.not_empty")
	if err := svc.Delete(ctx, admin, sorting.ID); err != nil {
		t.Fatal(err)
	}

	// Every write left an audit row and an outbox event.
	var audits, events int
	_ = d.Primary.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE entity='category' AND actor_id=$1", admin.UserID).Scan(&audits)
	_ = d.Primary.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE topic='category.changed' AND aggregate_id = ANY($1)",
		[]int64{root.ID, algo.ID, search.ID, sorting.ID, other.ID}).Scan(&events)
	if audits != 7 || events != 7 {
		t.Fatalf("audits=%d events=%d, want 7 each", audits, events)
	}
}

func TestCategoryDepthLimit(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d, access.NewPolicy(d.Primary))
	ctx := context.Background()
	admin := testenv.Admin(t, d)

	parent, err := svc.Create(ctx, admin, CreateCategory{Slug: testenv.Unique("deep"), Name: "L1"})
	if err != nil {
		t.Fatal(err)
	}
	for level := 2; level <= MaxDepth; level++ {
		if parent, err = svc.Create(ctx, admin, CreateCategory{ParentID: &parent.ID, Slug: "l", Name: "L"}); err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
	}
	_, err = svc.Create(ctx, admin, CreateCategory{ParentID: &parent.ID, Slug: "l", Name: "Too deep"})
	wantKind(t, err, apperr.KindInvalid, "category.too_deep")
}

func TestCategoryPermissions(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d, access.NewPolicy(d.Primary))
	ctx := context.Background()
	admin := testenv.Admin(t, d)

	dsa, _ := svc.Create(ctx, admin, CreateCategory{Slug: testenv.Unique("dsa"), Name: "DSA"})
	web, _ := svc.Create(ctx, admin, CreateCategory{Slug: testenv.Unique("web"), Name: "Web"})

	reader := testenv.User(t, d)
	_, err := svc.Create(ctx, reader, CreateCategory{ParentID: &dsa.ID, Slug: "x", Name: "X"})
	wantKind(t, err, apperr.KindForbidden, "access.denied")

	// An editor scoped to DSA manages the DSA subtree only, and cannot add top-level sections.
	editor := testenv.User(t, d)
	testenv.Grant(t, d, editor, "EDITOR", &dsa.ID)
	arrays, err := svc.Create(ctx, editor, CreateCategory{ParentID: &dsa.ID, Slug: "arrays", Name: "Arrays"})
	if err != nil {
		t.Fatalf("scoped editor in scope: %v", err)
	}
	if _, err := svc.Create(ctx, editor, CreateCategory{ParentID: &arrays.ID, Slug: "two-pointers", Name: "Two pointers"}); err != nil {
		t.Fatalf("scoped editor in a descendant: %v", err)
	}
	_, err = svc.Create(ctx, editor, CreateCategory{ParentID: &web.ID, Slug: "x", Name: "X"})
	wantKind(t, err, apperr.KindForbidden, "")
	_, err = svc.Create(ctx, editor, CreateCategory{Slug: testenv.Unique("root"), Name: "Root"})
	wantKind(t, err, apperr.KindForbidden, "")
	_, err = svc.Move(ctx, editor, arrays.ID, MoveCategory{ParentID: &web.ID, Slug: "arrays"})
	wantKind(t, err, apperr.KindForbidden, "")
}

func TestTags(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d, access.NewPolicy(d.Primary))
	ctx := context.Background()
	admin := testenv.Admin(t, d)

	var created []Tag
	for _, name := range []string{"Zig", "Ada", "Haskell", "Erlang", "Dynamic Programming"} {
		kind := TagLanguage
		if name == "Dynamic Programming" {
			kind = TagPattern
		}
		tag, err := svc.CreateTag(ctx, admin, CreateTag{Kind: kind, Slug: testenv.Unique("t"), Name: name})
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, tag)
	}
	_, err := svc.CreateTag(ctx, admin, CreateTag{Kind: TagLanguage, Slug: created[0].Slug, Name: "Dup"})
	wantKind(t, err, apperr.KindConflict, "tag.slug_taken")

	// Keyset pages walk the alphabet in order, never repeating a tag. The test database keeps
	// tags from earlier runs, so check ordering and membership rather than an exact list.
	var listed []Tag
	req := page.Request[TagCursor]{Limit: 2}
	for {
		res, err := svc.ListTags(ctx, TagLanguage, req)
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, res.Items...)
		if res.NextCursor == nil {
			break
		}
		last := res.Items[len(res.Items)-1]
		req.Key = &TagCursor{Name: last.Name, ID: last.ID}
	}
	seen := map[int64]bool{}
	for i, tg := range listed {
		if seen[tg.ID] {
			t.Fatalf("tag %d listed twice", tg.ID)
		}
		seen[tg.ID] = true
		if i > 0 && (listed[i-1].Name > tg.Name || (listed[i-1].Name == tg.Name && listed[i-1].ID > tg.ID)) {
			t.Fatalf("out of order at %d: %q then %q", i, listed[i-1].Name, tg.Name)
		}
	}
	for _, tg := range created[:4] {
		if !seen[tg.ID] {
			t.Fatalf("listing is missing %s", tg.Name)
		}
	}

	found, err := svc.SearchTags(ctx, "dynamc program", nil) // typo-tolerant
	if err != nil || !containsTag(found, created[4].ID) {
		t.Fatalf("trigram search: %v (%v)", found, err)
	}

	editor := testenv.User(t, d)
	testenv.Grant(t, d, editor, "EDITOR", nil)
	if _, err := svc.UpdateTag(ctx, editor, created[0].ID, UpdateTag{Name: "Zig lang"}); err != nil {
		t.Fatal(err)
	}
	err = svc.DeleteTag(ctx, editor, created[0].ID)
	wantKind(t, err, apperr.KindForbidden, "")
	if err := svc.DeleteTag(ctx, admin, created[0].ID); err != nil {
		t.Fatal(err)
	}
}

func containsTag(tags []Tag, id int64) bool {
	for _, t := range tags {
		if t.ID == id {
			return true
		}
	}
	return false
}

// Root categories can never take a site route's name: /{root}/{slug} would collide with it.
func TestReservedRootSlugs(t *testing.T) {
	d := testenv.DB(t)
	svc := NewService(d, access.NewPolicy(d.Primary))
	ctx := context.Background()
	admin := testenv.Admin(t, d)
	for _, slug := range []string{"videos", "shorts", "blog", "interview"} {
		if _, err := svc.Create(ctx, admin, CreateCategory{Slug: slug, Name: slug}); err == nil {
			t.Fatalf("root category %q accepted", slug)
		}
	}
}
