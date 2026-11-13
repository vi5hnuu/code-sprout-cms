package library_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/arenatest"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/library"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/problem"
)

// Positions are per reader, the latest one wins, finishing is sticky, the two lists page
// independently, and erase and prune remove rows.
func TestReadingProgress(t *testing.T) {
	e := arenatest.New(t)
	ctx := context.Background()
	publish := func() int64 {
		tag := testenv.Unique("read")
		return e.Publish(tag, []problem.Sample{{Input: "1", Output: "1"}}, []judge.TestCase{{Stdin: tag, Expected: "1"}}, nil)
	}
	a, b, c := publish(), publish(), publish()
	progress := library.NewProgress(e.DB, e.Reader)
	reader, other := testenv.User(t, e.DB), testenv.User(t, e.DB)
	h := func(s string) *string { return &s }

	// Validation.
	if _, err := progress.Save(ctx, reader, a, nil, 101); code(err) != "validation_failed" {
		t.Errorf("percent 101: %v", err)
	}
	if _, err := progress.Save(ctx, reader, a, h("two words"), 10); code(err) != "validation_failed" {
		t.Errorf("heading with a space: %v", err)
	}
	if _, err := progress.Save(ctx, reader, 1<<46, nil, 10); code(err) != "content.not_found" {
		t.Errorf("missing page: %v", err)
	}

	// Latest position wins; finishing is sticky.
	if _, err := progress.Save(ctx, reader, a, h("intro"), 20); err != nil {
		t.Fatal(err)
	}
	pos, err := progress.Save(ctx, reader, a, h("binary-search"), 95)
	if err != nil || pos.FinishedAt == nil || *pos.HeadingID != "binary-search" {
		t.Fatalf("finish = %+v, %v", pos, err)
	}
	pos, err = progress.Save(ctx, reader, a, nil, 5) // re-opened at the top
	if err != nil || pos.FinishedAt == nil || pos.Percent != 5 || pos.HeadingID != nil {
		t.Fatalf("re-open after finishing = %+v, %v; want finished kept, position updated", pos, err)
	}
	for _, id := range []int64{b, c} {
		if _, err := progress.Save(ctx, reader, id, h("x"), 40); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := progress.Save(ctx, other, b, nil, 50); err != nil {
		t.Fatal(err)
	}

	list := func(state library.ReadingState) []int64 {
		t.Helper()
		var ids []int64
		req := page.Request[library.ReadingCursor]{Limit: 1}
		for {
			pg, err := progress.List(ctx, reader, state, req)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range pg.Items {
				ids = append(ids, r.Item.ID)
			}
			if pg.NextCursor == nil {
				return ids
			}
			last := pg.Items[len(pg.Items)-1]
			at := last.UpdatedAt
			if state == library.ReadingFinished {
				at = *last.FinishedAt
			}
			req.Key = &library.ReadingCursor{At: at, ID: last.ContentID}
		}
	}
	if got := list(library.ReadingInProgress); len(got) != 2 || got[0] != c || got[1] != b {
		t.Errorf("in progress = %v, want [%d %d]", got, c, b)
	}
	if got := list(library.ReadingFinished); len(got) != 1 || got[0] != a {
		t.Errorf("finished = %v, want [%d]", got, a)
	}

	states, err := progress.States(ctx, reader, []int64{a, b, 1 << 46})
	if err != nil || len(states) != 2 {
		t.Fatalf("states = %+v, %v; want 2", states, err)
	}

	// Forget, erase, prune.
	if err := progress.Forget(ctx, reader, c); err != nil {
		t.Fatal(err)
	}
	if got := list(library.ReadingInProgress); len(got) != 1 || got[0] != b {
		t.Errorf("after forget = %v, want [%d]", got, b)
	}
	if err := e.DB.InTx(ctx, func(tx pgx.Tx) error { return library.EraseUserReading(ctx, tx, reader.UserID) }); err != nil {
		t.Fatal(err)
	}
	if got := list(library.ReadingFinished); len(got) != 0 {
		t.Errorf("after erase = %v, want none", got)
	}
	if _, err := e.DB.Primary.Exec(ctx, "UPDATE reading_progress SET updated_at = $1 WHERE user_id = $2", time.Now().AddDate(-2, 0, 0), other.UserID); err != nil {
		t.Fatal(err)
	}
	if err := progress.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := progress.States(ctx, other, []int64{b}); len(s) != 0 {
		t.Errorf("year-old position survived the prune: %+v", s)
	}
}
