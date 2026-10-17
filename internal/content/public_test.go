package content

import (
	"context"
	"testing"

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/user"
)

func (f *fixture) reader() *Reader {
	assets := asset.NewService(f.d, nil, asset.Config{PublicBucket: "sprout-public", PublicBaseURL: "https://cdn.test"})
	return NewReader(f.d, assets, f.tax, user.NewService(f.d.Primary, f.d.Replica, assets), engagement.NewStatsReader(f.d))
}

// publish creates and publishes an item through the real workflow.
func (f *fixture) publish(slug string, mutate func(*DraftInput)) Draft {
	f.t.Helper()
	in := f.publishableDraft(slug, f.readyAsset())
	if mutate != nil {
		mutate(&in)
	}
	d, err := f.svc.Create(context.Background(), f.author, CreateInput{Type: TypeArticle, DraftInput: in})
	if err != nil {
		f.t.Fatal(err)
	}
	if d, err = f.svc.Publish(context.Background(), f.editor, d.ID); err != nil {
		f.t.Fatal(err)
	}
	return d
}

func TestPublicPage(t *testing.T) {
	f := newFixture(t)
	r := f.reader()
	ctx := context.Background()
	slug := testenv.Unique("page")
	d := f.publish(slug, nil)

	p, redirect, err := r.Page(ctx, TypeArticle, "en", slug)
	if err != nil || redirect != nil {
		t.Fatalf("page = %v, %v", redirect, err)
	}
	if p.Body == nil || p.Locked || len(p.TOC) != 1 || p.ReadingMinutes < 1 {
		t.Fatalf("page body/toc = %+v", p)
	}
	if len(p.Assets) != 1 || len(p.Tags) != 1 || len(p.Breadcrumb) != 1 || p.Breadcrumb[0].ID != f.category.ID {
		t.Fatalf("hydration: assets=%d tags=%d breadcrumb=%+v", len(p.Assets), len(p.Tags), p.Breadcrumb)
	}
	for _, a := range p.Assets {
		if a.URL == nil || a.Width == nil {
			t.Fatalf("asset not resolvable: %+v", a)
		}
	}
	if p.Author.ID != f.author.UserID || p.Article == nil {
		t.Fatalf("author/article = %+v %+v", p.Author, p.Article)
	}

	// A draft edit is invisible until published.
	edited := f.publishableDraft(slug+"-v2", f.readyAsset())
	edited.Title = "Unpublished title"
	if _, err := f.svc.SaveDraft(ctx, f.author, d.ID, d.DraftRevNo, edited); err != nil {
		t.Fatal(err)
	}
	if p, _, _ = r.Page(ctx, TypeArticle, "en", slug); p.Title == "Unpublished title" {
		t.Fatal("public page shows the unpublished draft")
	}
	if _, err := f.svc.Publish(ctx, f.editor, d.ID); err != nil {
		t.Fatal(err)
	}
	_, redirect, err = r.Page(ctx, TypeArticle, "en", slug)
	if err != nil || redirect == nil || redirect.Slug != slug+"-v2" {
		t.Fatalf("old slug should redirect: %+v, %v", redirect, err)
	}

	_, _, err = r.Page(ctx, TypeArticle, "en", "never-existed-"+slug)
	if e, ok := apperr.As(err); !ok || e.Kind != apperr.KindNotFound {
		t.Fatalf("missing page err = %v", err)
	}
	_, _, err = r.Page(ctx, TypeBlog, "en", slug+"-v2")
	if err == nil {
		t.Fatal("slug resolved under the wrong type")
	}
}

func TestVisibilityAndAccess(t *testing.T) {
	f := newFixture(t)
	r := f.reader()
	ctx := context.Background()
	unlisted := f.publish(testenv.Unique("unlisted"), func(in *DraftInput) { in.Visibility = VisibilityUnlisted })
	private := f.publish(testenv.Unique("private"), func(in *DraftInput) { in.Visibility = VisibilityPrivate })
	premium := f.publish(testenv.Unique("premium"), func(in *DraftInput) { in.Access = AccessPremium })

	if p, _, err := r.Page(ctx, TypeArticle, "en", unlisted.Live.Slug); err != nil || !p.Unlisted {
		t.Fatalf("unlisted page = %+v, %v", p, err)
	}
	if _, _, err := r.Page(ctx, TypeArticle, "en", private.Live.Slug); err == nil {
		t.Fatal("private page is publicly readable")
	}
	p, _, err := r.Page(ctx, TypeArticle, "en", premium.Live.Slug)
	if err != nil || !p.Locked || p.Body != nil || len(p.TOC) == 0 {
		t.Fatalf("premium page should be locked with its outline visible: %+v, %v", p, err)
	}

	list, err := r.List(ctx, ListFilter{Category: f.category.Path}, page.Request[PublishedCursor]{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if containsItem(list.Items, unlisted.ID) || containsItem(list.Items, private.ID) || !containsItem(list.Items, premium.ID) {
		t.Fatalf("category listing visibility wrong: %v", ids(list.Items))
	}
	byTag, err := r.List(ctx, ListFilter{Tag: "topic:" + f.tagSlug()}, page.Request[PublishedCursor]{Limit: 50})
	if err != nil || containsItem(byTag.Items, unlisted.ID) || !containsItem(byTag.Items, premium.ID) {
		t.Fatalf("tag listing visibility wrong: %v, %v", ids(byTag.Items), err)
	}
}

func TestListingsPaginateAndExcludeArchived(t *testing.T) {
	f := newFixture(t)
	r := f.reader()
	ctx := context.Background()
	var published []Draft
	for range 5 {
		published = append(published, f.publish(testenv.Unique("list"), nil))
	}
	if _, err := f.svc.Archive(ctx, f.editor, published[2].ID); err != nil {
		t.Fatal(err)
	}

	var seen []int64
	req := page.Request[PublishedCursor]{Limit: 2}
	for {
		res, err := r.List(ctx, ListFilter{Category: f.category.Path}, req)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, ids(res.Items)...)
		if res.NextCursor == nil {
			break
		}
		last := res.Items[len(res.Items)-1]
		req.Key = &PublishedCursor{PublishedAt: last.PublishedAt, ID: last.ID}
	}
	want := []int64{published[4].ID, published[3].ID, published[1].ID, published[0].ID}
	if len(seen) != len(want) {
		t.Fatalf("seen %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen %v, want newest-first %v", seen, want)
		}
	}

	for _, filter := range []ListFilter{
		{Type: ptr(TypeArticle), Locale: "en"},
		{Tag: "topic:" + f.tagSlug()},
	} {
		res, err := r.List(ctx, filter, page.Request[PublishedCursor]{Limit: 100})
		if err != nil || !containsItem(res.Items, published[4].ID) || containsItem(res.Items, published[2].ID) {
			t.Fatalf("filter %+v: %v, %v", filter, ids(res.Items), err)
		}
		for _, it := range res.Items {
			if it.ID == published[4].ID && (it.Author.ID != f.author.UserID || it.ReadingMinutes == 0 || it.Category == nil) {
				t.Fatalf("list item not hydrated: %+v", it)
			}
		}
	}

	if _, err := r.List(ctx, ListFilter{Category: "x", Tag: "topic:y"}, page.Request[PublishedCursor]{Limit: 5}); err == nil {
		t.Fatal("conflicting filters accepted")
	}
	if _, err := r.List(ctx, ListFilter{}, page.Request[PublishedCursor]{Limit: 5}); err == nil {
		t.Fatal("unfiltered listing without a type accepted")
	}
}

func (f *fixture) tagSlug() string {
	f.t.Helper()
	var slug string
	if err := f.d.Primary.QueryRow(context.Background(), "SELECT slug FROM tag WHERE id=$1", f.tagID).Scan(&slug); err != nil {
		f.t.Fatal(err)
	}
	return slug
}

func containsItem(items []ListItem, id int64) bool {
	for _, i := range items {
		if i.ID == id {
			return true
		}
	}
	return false
}

func ids(items []ListItem) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func ptr[T any](v T) *T { return &v }
