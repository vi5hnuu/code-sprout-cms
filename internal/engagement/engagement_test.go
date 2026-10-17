package engagement

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/user"
)

// gate treats exactly the given ids as live, standing in for content.Reader.
type gate map[int64]bool

func (g gate) LiveIDs(_ context.Context, ids []int64) ([]int64, error) {
	out := []int64{}
	for _, id := range ids {
		if g[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

type env struct {
	t    *testing.T
	d    *db.DB
	svc  *Service
	live gate
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testenv.DB(t)
	rdb := testenv.Redis(t)
	files := asset.NewService(d, nil, asset.Config{})
	live := gate{}
	return &env{t: t, d: d, live: live,
		svc: NewService(d, rdb, live, user.NewService(d.Primary, d.Replica, files), access.NewPolicy(d.Primary))}
}

// item inserts a minimal published item with its stats row and marks it live. It is UNLISTED
// so these bare rows never show up in other packages' listing tests (packages share the test
// database and run in parallel).
func (e *env) item() int64 {
	e.t.Helper()
	author := testenv.User(e.t, e.d)
	ctx := context.Background()
	var id int64
	err := e.d.Primary.QueryRow(ctx, `
		INSERT INTO content (type, slug, title, author_id, status, visibility, published_rev_no, published_at, last_published_at)
		VALUES ('ARTICLE', $1, 'Item', $2, 'PUBLISHED', 'UNLISTED', 1, now(), now()) RETURNING id`,
		testenv.Unique("item"), author.UserID).Scan(&id)
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("x"))
	if _, err := e.d.Primary.Exec(ctx, `INSERT INTO content_revision (content_id, rev_no, body_md, body_sha256, editor_id) VALUES ($1, 1, 'x', $2, $3)`, id, sum[:], author.UserID); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.d.Primary.Exec(ctx, `INSERT INTO content_stats (content_id) VALUES ($1)`, id); err != nil {
		e.t.Fatal(err)
	}
	e.live[id] = true
	return id
}

func (e *env) stats(id int64) Stats {
	e.t.Helper()
	m, err := NewStatsReader(e.d).StatsFor(context.Background(), []int64{id})
	if err != nil {
		e.t.Fatal(err)
	}
	return m[id]
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	if e, ok := apperr.As(err); !ok || e.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func TestLikesAreIdempotentAndCounted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.item()
	u := testenv.User(t, e.d)

	for range 2 {
		if err := e.svc.Like(ctx, u, id); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.stats(id); s.Likes != 1 {
		t.Fatalf("likes = %d after a double like", s.Likes)
	}
	for range 2 {
		if err := e.svc.Unlike(ctx, u, id); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.stats(id); s.Likes != 0 {
		t.Fatalf("likes = %d after a double unlike", s.Likes)
	}
	wantCode(t, e.svc.Like(ctx, u, 999999999), "content.not_found")

	// Many readers at once: every like counts exactly once.
	var wg sync.WaitGroup
	for range 20 {
		reader := testenv.User(t, e.d)
		wg.Go(func() {
			if err := e.svc.Like(ctx, reader, id); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if s := e.stats(id); s.Likes != 20 {
		t.Fatalf("likes = %d, want 20", s.Likes)
	}
}

func TestBookmarksListsAndState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := e.item(), e.item()
	u := testenv.User(t, e.d)

	list, err := e.svc.CreateBookmarkList(ctx, u, "Interview prep")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateBookmarkList(ctx, u, "Interview prep")
	wantCode(t, err, "bookmark.list_exists")
	other := testenv.User(t, e.d)
	wantCode(t, e.svc.Bookmark(ctx, other, a, &list.ID), "validation_failed")

	if err := e.svc.Bookmark(ctx, u, a, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Bookmark(ctx, u, a, &list.ID); err != nil { // moves, does not double count
		t.Fatal(err)
	}
	if err := e.svc.Bookmark(ctx, u, b, nil); err != nil {
		t.Fatal(err)
	}
	if s := e.stats(a); s.Bookmarks != 1 {
		t.Fatalf("bookmarks = %d", s.Bookmarks)
	}
	if err := e.svc.Like(ctx, u, b); err != nil {
		t.Fatal(err)
	}

	st, err := e.svc.State(ctx, u, []int64{a, b, 424242})
	if err != nil {
		t.Fatal(err)
	}
	if !st[a].Bookmarked || st[a].BookmarkListID == nil || *st[a].BookmarkListID != list.ID || st[a].Liked {
		t.Fatalf("state a = %+v", st[a])
	}
	if !st[b].Liked || !st[b].Bookmarked || st[424242].Liked {
		t.Fatalf("state b = %+v, unknown = %+v", st[b], st[424242])
	}

	inList, err := e.svc.Bookmarks(ctx, u, &list.ID, page.Request[EntryCursor]{Limit: 10})
	if err != nil || len(inList.Items) != 1 || inList.Items[0].ContentID != a {
		t.Fatalf("list bookmarks = %+v, %v", inList.Items, err)
	}
	first, err := e.svc.Bookmarks(ctx, u, nil, page.Request[EntryCursor]{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil || first.Items[0].ContentID != b {
		t.Fatalf("page 1 = %+v, %v", first, err)
	}
	cur := entryCursor(first.Items[0])
	second, _ := e.svc.Bookmarks(ctx, u, nil, page.Request[EntryCursor]{Limit: 1, Key: &cur})
	if len(second.Items) != 1 || second.Items[0].ContentID != a {
		t.Fatalf("page 2 = %+v", second.Items)
	}

	if err := e.svc.DeleteBookmarkList(ctx, u, list.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ = e.svc.State(ctx, u, []int64{a}); !st[a].Bookmarked || st[a].BookmarkListID != nil {
		t.Fatalf("deleting a list must keep its bookmarks: %+v", st[a])
	}
	if err := e.svc.Unbookmark(ctx, u, a); err != nil {
		t.Fatal(err)
	}
	if s := e.stats(a); s.Bookmarks != 0 {
		t.Fatalf("bookmarks = %d after removal", s.Bookmarks)
	}
}

func TestCommentLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := e.item()
	alice, bob := testenv.User(t, e.d), testenv.User(t, e.d)

	_, err := e.svc.CreateComment(ctx, auth.Principal{UserID: alice.UserID, IsGuest: true}, id, nil, "hi")
	wantCode(t, err, "comment.guest")
	_, err = e.svc.CreateComment(ctx, alice, id, nil, "<script>x</script>")
	wantCode(t, err, "comment.invalid")

	root, err := e.svc.CreateComment(ctx, alice, id, nil, "Why does `mid` overflow?")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := e.svc.CreateComment(ctx, bob, id, &root.ID, "Because lo+hi can exceed int max.")
	if err != nil {
		t.Fatal(err)
	}
	nested, err := e.svc.CreateComment(ctx, alice, id, &reply.ID, "Thanks!")
	if err != nil {
		t.Fatal(err)
	}
	if nested.RootID == nil || *nested.RootID != root.ID || *nested.ParentID != reply.ID {
		t.Fatalf("a reply to a reply joins the thread: %+v", nested)
	}
	if s := e.stats(id); s.Comments != 3 {
		t.Fatalf("comments = %d", s.Comments)
	}

	_, err = e.svc.EditComment(ctx, bob, id, root.ID, "hijack")
	wantCode(t, err, "comment.not_found")
	if edited, err := e.svc.EditComment(ctx, alice, id, root.ID, "Why does `mid` overflow in C++?"); err != nil || edited.EditedAt == nil {
		t.Fatalf("edit = %+v, %v", edited, err)
	}

	// Deleting a root that has replies keeps the thread, shown as deleted.
	if err := e.svc.DeleteComment(ctx, alice, id, root.ID); err != nil {
		t.Fatal(err)
	}
	thread, err := e.svc.Thread(ctx, id, page.Request[CommentCursor]{Limit: 10})
	if err != nil || len(thread.Items) != 1 || !thread.Items[0].Deleted || thread.Items[0].Body != "" || thread.Items[0].Author != nil {
		t.Fatalf("thread = %+v, %v", thread.Items, err)
	}
	replies, err := e.svc.Replies(ctx, id, root.ID, page.Request[CommentCursor]{Limit: 10})
	if err != nil || len(replies.Items) != 2 || replies.Items[0].ID != reply.ID {
		t.Fatalf("replies = %+v, %v", replies.Items, err)
	}

	// Three distinct reports hide a comment until a moderator restores it.
	wantCode(t, e.svc.ReportComment(ctx, bob, id, reply.ID, "SPAM", nil), "comment.self_report")
	for range 3 {
		if err := e.svc.ReportComment(ctx, testenv.User(t, e.d), id, reply.ID, "SPAM", nil); err != nil {
			t.Fatal(err)
		}
	}
	if s := e.stats(id); s.Comments != 1 {
		t.Fatalf("comments = %d after delete + auto-hide, want 1", s.Comments)
	}
	_, err = e.svc.Reports(ctx, bob, page.Request[ReportCursor]{Limit: 10})
	wantCode(t, err, "access.denied")
	mod := testenv.User(t, e.d)
	testenv.Grant(t, e.d, mod, "EDITOR", nil)
	reports, err := e.svc.Reports(ctx, mod, page.Request[ReportCursor]{Limit: 100})
	if err != nil || len(reports.Items) < 3 {
		t.Fatalf("reports = %d, %v", len(reports.Items), err)
	}
	if err := e.svc.Moderate(ctx, mod, id, reply.ID, ModerateRestore); err != nil {
		t.Fatal(err)
	}
	if s := e.stats(id); s.Comments != 2 {
		t.Fatalf("comments = %d after restore, want 2", s.Comments)
	}
	if n := countRows(t, e.d, "SELECT count(*) FROM comment_report WHERE comment_id=$1 AND resolved_at IS NULL", reply.ID); n != 0 {
		t.Fatalf("%d reports still open after moderation", n)
	}

	delete(e.live, id)
	_, err = e.svc.CreateComment(ctx, alice, id, nil, "late")
	wantCode(t, err, "content.not_found")
}

func TestViewsAndSharesAreBufferedAndDeduplicated(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, gone := e.item(), e.item()
	delete(e.live, gone) // its hits must be dropped at flush time

	// Dedupe keys live in Redis, which outlasts test-database resets (and reused ids), so
	// viewers carry a per-run suffix.
	run := testenv.Unique("run")
	for _, viewer := range []string{"ip1", "ip1", "ip2", "u7"} {
		if err := e.svc.RecordView(ctx, a, viewer+run); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.RecordView(ctx, gone, "ip1"+run); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []string{"copy", "copy", "x"} {
		if err := e.svc.RecordShare(ctx, a, "ip1"+run, ch); err != nil {
			t.Fatal(err)
		}
	}
	wantCode(t, e.svc.RecordShare(ctx, a, "ip1"+run, "myspace"), "validation_failed")

	if s := e.stats(a); s.Views != 0 {
		t.Fatalf("views written before flush: %d", s.Views)
	}
	if err := e.svc.FlushCounters(ctx); err != nil {
		t.Fatal(err)
	}
	if s := e.stats(a); s.Views != 3 || s.Shares != 2 {
		t.Fatalf("after flush: views=%d shares=%d, want 3 and 2", s.Views, s.Shares)
	}
	if s := e.stats(gone); s.Views != 0 {
		t.Fatalf("hits on a non-live item were applied: %d", s.Views)
	}
	if n := countRows(t, e.d, "SELECT views FROM content_daily_stats WHERE content_id=$1 AND day=current_date", a); n != 3 {
		t.Fatalf("daily views = %d", n)
	}

	// A batch detached by a worker that died before applying it is picked up next time.
	if err := e.svc.RecordView(ctx, a, "ip9"+run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.detach(ctx, counterViews); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.FlushCounters(ctx); err != nil {
		t.Fatal(err)
	}
	if s := e.stats(a); s.Views != 4 {
		t.Fatalf("leftover batch not applied: views=%d", s.Views)
	}
}

func countRows(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Primary.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
