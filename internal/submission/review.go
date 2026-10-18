package submission

import (
	"context"
	"time"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/page"
	"code-sprout-cms/internal/submission/submissiondb"
)

// Spaced review. Solving a problem once proves little; solving it again a day, then days and
// weeks later is what makes a pattern stick. Every Accepted submission schedules its problem:
//
//   - first solve: due again in reviewIntervals[0];
//   - Accepted while due: one step further; after the last step the problem is mastered;
//   - Accepted before it is due: no change (re-solving from memory minutes later proves nothing);
//   - any other verdict on a scheduled problem: one step back and due again tomorrow.
//
// Readers can dismiss a problem; dismissed and mastered problems are never rescheduled.
var reviewIntervals = []time.Duration{
	1 * day, 3 * day, 7 * day, 14 * day, 30 * day, 60 * day,
}

const day = 24 * time.Hour

const (
	reviewActive    = "ACTIVE"
	reviewMastered  = "MASTERED"
	reviewDismissed = "DISMISSED"
)

// scheduleReview moves the problem's review after a judged submission. It runs inside the
// transaction that records the verdict, so progress and schedule always agree.
func scheduleReview(ctx context.Context, q *submissiondb.Queries, userID, problemID int64, accepted bool, now time.Time) error {
	cur, err := q.GetReviewForUpdate(ctx, submissiondb.GetReviewForUpdateParams{UserID: userID, ProblemID: problemID})
	exists := err == nil
	if err != nil && !db.IsNoRows(err) {
		return err
	}
	set := func(state string, step int, due time.Time) error {
		return q.UpsertReview(ctx, submissiondb.UpsertReviewParams{UserID: userID, ProblemID: problemID, State: state, Step: int16(step), DueAt: due})
	}
	switch {
	case !exists:
		if !accepted {
			return nil // nothing to review until it has been solved once
		}
		return set(reviewActive, 0, now.Add(reviewIntervals[0]))
	case cur.State != reviewActive:
		return nil
	case !accepted:
		return set(reviewActive, max(int(cur.Step)-1, 0), now.Add(day))
	case cur.DueAt.After(now):
		return nil
	case int(cur.Step) >= len(reviewIntervals)-1:
		return set(reviewMastered, int(cur.Step), cur.DueAt)
	default:
		next := int(cur.Step) + 1
		return set(reviewActive, next, now.Add(reviewIntervals[next]))
	}
}

// ReviewCursor is the keyset position in due order.
type ReviewCursor struct {
	DueAt     time.Time `json:"d"`
	ProblemID int64     `json:"p"`
}

type ReviewItem struct {
	Problem content.ListItem `json:"problem"`
	// Step counts the intervals passed so far (0 = first review); Steps is how many there are.
	Step  int       `json:"step"`
	Steps int       `json:"steps"`
	DueAt time.Time `json:"dueAt"`
}

type ReviewPage struct {
	page.Page[ReviewItem]
	// DueCount is how many reviews are due now in total (the badge), whatever page this is.
	DueCount int64 `json:"dueCount"`
}

// Reviews lists the reader's scheduled problems: due now (oldest first) or coming up.
func (s *Service) Reviews(ctx context.Context, p auth.Principal, due bool, req page.Request[ReviewCursor]) (ReviewPage, error) {
	q := submissiondb.New(s.db.Replica)
	var afterDue *time.Time
	var afterID *int64
	if req.Key != nil {
		afterDue, afterID = &req.Key.DueAt, &req.Key.ProblemID
	}
	type row struct {
		problemID int64
		step      int16
		dueAt     time.Time
	}
	var rows []row
	if due {
		rs, err := q.ListDueReviews(ctx, submissiondb.ListDueReviewsParams{UserID: p.UserID, AfterDue: afterDue, AfterID: afterID, Lim: int32(req.Limit + 1)})
		if err != nil {
			return ReviewPage{}, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.ProblemID, r.Step, r.DueAt})
		}
	} else {
		rs, err := q.ListUpcomingReviews(ctx, submissiondb.ListUpcomingReviewsParams{UserID: p.UserID, AfterDue: afterDue, AfterID: afterID, Lim: int32(req.Limit + 1)})
		if err != nil {
			return ReviewPage{}, err
		}
		for _, r := range rs {
			rows = append(rows, row{r.ProblemID, r.Step, r.DueAt})
		}
	}
	pg := page.Build(rows, req.Limit, func(r row) ReviewCursor { return ReviewCursor{DueAt: r.dueAt, ProblemID: r.problemID} })

	ids := make([]int64, len(pg.Items))
	for i, r := range pg.Items {
		ids[i] = r.problemID
	}
	items, err := s.content.ItemsByID(ctx, ids)
	if err != nil {
		return ReviewPage{}, err
	}
	byID := make(map[int64]content.ListItem, len(items))
	for _, it := range items {
		byID[it.ID] = it
	}
	out := ReviewPage{Page: page.Page[ReviewItem]{Items: make([]ReviewItem, 0, len(pg.Items)), NextCursor: pg.NextCursor}}
	for _, r := range pg.Items {
		if it, ok := byID[r.problemID]; ok { // unpublished problems drop out quietly
			out.Items = append(out.Items, ReviewItem{Problem: it, Step: int(r.step), Steps: len(reviewIntervals), DueAt: r.dueAt})
		}
	}
	if out.DueCount, err = q.CountDueReviews(ctx, p.UserID); err != nil {
		return ReviewPage{}, err
	}
	return out, nil
}

// DismissReview stops reviewing a problem for good.
func (s *Service) DismissReview(ctx context.Context, p auth.Principal, problemID int64) error {
	n, err := submissiondb.New(s.db.Primary).DismissReview(ctx, submissiondb.DismissReviewParams{UserID: p.UserID, ProblemID: problemID})
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.NotFound("review.not_found", "That problem is not scheduled for review.")
	}
	return nil
}
