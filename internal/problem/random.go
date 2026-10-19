package problem

import (
	"cmp"
	"context"
	"math/rand/v2"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/problem/problemdb"
)

// RandomFilter narrows a random pick; empty fields mean any.
type RandomFilter struct {
	Difficulty *Difficulty
	Tag        string // kind:slug, e.g. company:google
}

var errNoRandom = apperr.NotFound("problem.none_match", "No problem matches these filters yet.")

// A tagged pick reads the tag in batches from the content domain and keeps the first listed
// problem; the scan is capped so one request never walks a huge tag end to end.
const (
	taggedBatch      = 100
	taggedMaxBatches = 10
)

// Random returns one listed problem matching the filter (interview mode, "pick for me").
//
// It picks a random id between the smallest and largest matching ids and takes the first
// match at or after it, wrapping to the smallest: two index lookups whatever the catalogue
// size. Problems that follow a gap in the id space are a little more likely to come up; for
// "surprise me" that is fine, and it avoids reading every row as ORDER BY random() would.
func (s *Service) Random(ctx context.Context, f RandomFilter) (ListItem, error) {
	if f.Difficulty != nil && !f.Difficulty.valid() {
		return ListItem{}, apperr.ValidationFailed(map[string]string{"difficulty": "difficulty must be EASY, MEDIUM or HARD."})
	}
	q := problemdb.New(s.db.Replica)
	var diff *string
	if f.Difficulty != nil {
		d := string(*f.Difficulty)
		diff = &d
	}

	var lo, hi int64
	var from func(pivot int64) (int64, error)
	switch {
	case f.Tag != "":
		tag, err := s.reader.ResolveTag(ctx, f.Tag)
		if err != nil {
			return ListItem{}, err
		}
		if lo, hi, err = s.reader.TagBounds(ctx, tag.ID); err != nil {
			return ListItem{}, err
		}
		from = func(p int64) (int64, error) { return s.taggedFrom(ctx, q, tag.ID, diff, p) }
	case diff != nil:
		b, err := q.ListedIDBoundsByDifficulty(ctx, *diff)
		if err != nil {
			return ListItem{}, err
		}
		lo, hi = b.Lo, b.Hi
		from = func(p int64) (int64, error) {
			return q.ListedProblemFromByDifficulty(ctx, problemdb.ListedProblemFromByDifficultyParams{Difficulty: *diff, Pivot: p})
		}
	default:
		b, err := q.ListedIDBounds(ctx)
		if err != nil {
			return ListItem{}, err
		}
		lo, hi = b.Lo, b.Hi
		from = func(p int64) (int64, error) { return q.ListedProblemFrom(ctx, p) }
	}
	if hi == 0 {
		return ListItem{}, errNoRandom
	}

	id, err := from(lo + rand.Int64N(hi-lo+1))
	if db.IsNoRows(err) { // nothing at or after the pivot (a tagged walk filtered by difficulty): wrap
		id, err = from(lo)
	}
	if db.IsNoRows(err) {
		return ListItem{}, errNoRandom
	}
	if err != nil {
		return ListItem{}, err
	}
	item, ok, err := s.liveItem(ctx, id)
	if err != nil {
		return ListItem{}, err
	}
	if !ok {
		return ListItem{}, errNoRandom
	}
	return item, nil
}

// taggedFrom is the first listed problem (of the difficulty, if given) with the tag, at or
// after pivot. It returns pgx.ErrNoRows when none is found within the scan cap, like the
// single-query pickers, so the caller wraps the same way.
func (s *Service) taggedFrom(ctx context.Context, q *problemdb.Queries, tagID int64, diff *string, pivot int64) (int64, error) {
	for range taggedMaxBatches {
		ids, err := s.reader.TaggedFrom(ctx, tagID, content.TypeProblem, pivot, taggedBatch)
		if err != nil || len(ids) == 0 {
			return 0, cmp.Or(err, pgx.ErrNoRows)
		}
		id, err := q.FirstListedAmong(ctx, problemdb.FirstListedAmongParams{Ids: ids, Difficulty: diff})
		if !db.IsNoRows(err) {
			return id, err
		}
		pivot = ids[len(ids)-1] + 1
	}
	return 0, pgx.ErrNoRows
}
