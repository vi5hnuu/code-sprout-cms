package account_test

import (
	"context"
	"testing"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/account"
	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/library"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/submission"
	"code-sprout-cms/internal/user"
)

// An erase removes everything personal, keeps counters consistent, anonymises the profile
// and leaves other readers' data alone.
func TestEraseRemovesPersonalDataAndKeepsOthers(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	tag := testenv.Unique("erase")
	pageID := e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)

	users := user.NewService(e.DB.Primary, e.DB.Replica, e.Assets)
	policy := access.NewPolicy(e.DB.Primary)
	eng := engagement.NewService(e.DB, testenv.Redis(t), e.Reader, users, policy)
	lists := collection.NewService(e.DB, e.Reader, policy)
	notes := library.NewNotes(e.DB, e.Reader)

	reader, other := testenv.User(t, e.DB), testenv.User(t, e.DB)
	for _, p := range []struct{ who, body string }{{"reader", "my comment"}, {"other", "their comment"}} {
		who := reader
		if p.who == "other" {
			who = other
		}
		if err := eng.Like(ctx, who, pageID); err != nil {
			t.Fatal(err)
		}
		if err := eng.Bookmark(ctx, who, pageID, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.CreateComment(ctx, who, pageID, nil, p.body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := notes.Put(ctx, reader, pageID, "private"); err != nil {
		t.Fatal(err)
	}
	l, err := lists.Create(ctx, reader, collection.Input{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	if err := lists.AddItem(ctx, reader, l.ID, pageID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DB.Primary.Exec(ctx, `INSERT INTO user_problem (user_id, problem_id, attempts, first_solved_at, last_submitted_at) VALUES ($1, $2, 1, now(), now())`, reader.UserID, pageID); err != nil {
		t.Fatal(err)
	}

	forgotten := ""
	svc := account.NewService(e.DB, func(sub string) { forgotten = sub },
		engagement.EraseUser, library.EraseUser, submission.EraseUser, collection.EraseUser, access.EraseUser, users.EraseUser)
	if err := svc.Erase(ctx, reader); err != nil {
		t.Fatal(err)
	}
	if forgotten != reader.Subject {
		t.Fatalf("cached identity not dropped: %q", forgotten)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := e.DB.Primary.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, q := range map[string]string{
		"reactions":    `SELECT count(*) FROM reaction WHERE user_id = $1`,
		"bookmarks":    `SELECT count(*) FROM bookmark WHERE user_id = $1`,
		"notes":        `SELECT count(*) FROM user_note WHERE user_id = $1`,
		"lists":        `SELECT count(*) FROM collection WHERE owner_id = $1 AND kind = 'LIST'`,
		"user_problem": `SELECT count(*) FROM user_problem WHERE user_id = $1`,
		"comment text": `SELECT count(*) FROM comment WHERE author_id = $1 AND (status <> 'DELETED' OR body_md <> '[deleted]')`,
	} {
		if n := count(q, reader.UserID); n != 0 {
			t.Errorf("%s left after erase: %d", name, n)
		}
	}
	if n := count(`SELECT count(*) FROM reaction WHERE user_id = $1`, other.UserID); n != 1 {
		t.Errorf("another reader's like was touched: %d", n)
	}
	s, err := engagement.NewStatsReader(e.DB).StatsFor(ctx, []int64{pageID})
	if err != nil {
		t.Fatal(err)
	}
	if got := s[pageID]; got.Likes != 1 || got.Bookmarks != 1 || got.Comments != 1 {
		t.Errorf("counters after erase = %+v, want 1 like, 1 save, 1 comment", got)
	}
	prof, err := users.Profile(ctx, reader.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if prof.DisplayName != "Former member" || prof.Handle != nil || prof.Bio != nil {
		t.Errorf("profile not anonymised: %+v", prof)
	}
}
