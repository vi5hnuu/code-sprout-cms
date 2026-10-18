package submission

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/platform/testenv"
	"code-sprout-cms/internal/submission/submissiondb"
)

// The schedule moves exactly as documented in review.go.
func TestReviewSchedule(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	user := testenv.User(t, d)
	const problemID = 987654321 // reviews reference problems by id only
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	step := func(accepted bool, at time.Time) submissiondb.GetReviewForUpdateRow {
		t.Helper()
		var got submissiondb.GetReviewForUpdateRow
		err := d.InTx(ctx, func(tx pgx.Tx) error {
			q := submissiondb.New(tx)
			if err := scheduleReview(ctx, q, user.UserID, problemID, accepted, at); err != nil {
				return err
			}
			var err error
			got, err = q.GetReviewForUpdate(ctx, submissiondb.GetReviewForUpdateParams{UserID: user.UserID, ProblemID: problemID})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	want := func(r submissiondb.GetReviewForUpdateRow, state string, s int, due time.Time) {
		t.Helper()
		if r.State != state || int(r.Step) != s || !r.DueAt.Equal(due) {
			t.Fatalf("review = %s step %d due %s, want %s step %d due %s", r.State, r.Step, r.DueAt, state, s, due)
		}
	}

	// A failed first attempt schedules nothing.
	if err := d.InTx(ctx, func(tx pgx.Tx) error {
		return scheduleReview(ctx, submissiondb.New(tx), user.UserID, problemID, false, t0)
	}); err != nil {
		t.Fatal(err)
	}
	r := step(true, t0) // first solve
	want(r, reviewActive, 0, t0.Add(day))
	want(step(true, t0.Add(time.Hour)), reviewActive, 0, t0.Add(day)) // early re-solve: no change

	at := t0.Add(day)
	r = step(true, at) // due: advance
	want(r, reviewActive, 1, at.Add(3*day))
	at = at.Add(3 * day)
	r = step(false, at) // forgot: one step back, tomorrow
	want(r, reviewActive, 0, at.Add(day))

	// Passing every interval on time masters the problem: one solve per remaining step.
	solves := 0
	for r.State == reviewActive {
		r = step(true, r.DueAt.Add(time.Minute))
		solves++
	}
	if r.State != reviewMastered || solves != len(reviewIntervals) {
		t.Fatalf("state %s after %d solves, want mastered after %d", r.State, solves, len(reviewIntervals))
	}
	want(step(false, r.DueAt.Add(day)), reviewMastered, int(r.Step), r.DueAt) // mastered stays put
}
