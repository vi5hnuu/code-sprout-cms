package problem

import (
	"context"
	"math/rand/v2"

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

// Random returns one listed problem matching the filter (interview mode, "pick for me").
//
// It picks a random id between the smallest and largest matching ids and takes the first
// match at or after it, wrapping to the smallest: two index lookups whatever the catalogue
// size. Problems that follow a gap in the id space are a little more likely to come up; for
// "surprise me" that is fine, and it avoids reading every row as ORDER BY random() would.
func (s *Service) Random(ctx context.Context, f RandomFilter) (content.ListItem, error) {
	if f.Difficulty != nil && !f.Difficulty.valid() {
		return content.ListItem{}, apperr.ValidationFailed(map[string]string{"difficulty": "difficulty must be EASY, MEDIUM or HARD."})
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
			return content.ListItem{}, err
		}
		b, err := q.TaggedIDBounds(ctx, tag.ID)
		if err != nil {
			return content.ListItem{}, err
		}
		lo, hi = b.Lo, b.Hi
		from = func(p int64) (int64, error) {
			return q.TaggedProblemFrom(ctx, problemdb.TaggedProblemFromParams{TagID: tag.ID, Pivot: p, Difficulty: diff})
		}
	case diff != nil:
		b, err := q.ListedIDBoundsByDifficulty(ctx, *diff)
		if err != nil {
			return content.ListItem{}, err
		}
		lo, hi = b.Lo, b.Hi
		from = func(p int64) (int64, error) {
			return q.ListedProblemFromByDifficulty(ctx, problemdb.ListedProblemFromByDifficultyParams{Difficulty: *diff, Pivot: p})
		}
	default:
		b, err := q.ListedIDBounds(ctx)
		if err != nil {
			return content.ListItem{}, err
		}
		lo, hi = b.Lo, b.Hi
		from = func(p int64) (int64, error) { return q.ListedProblemFrom(ctx, p) }
	}
	if hi == 0 {
		return content.ListItem{}, errNoRandom
	}

	id, err := from(lo + rand.Int64N(hi-lo+1))
	if db.IsNoRows(err) { // nothing at or after the pivot (a tagged walk filtered by difficulty): wrap
		id, err = from(lo)
	}
	if db.IsNoRows(err) {
		return content.ListItem{}, errNoRandom
	}
	if err != nil {
		return content.ListItem{}, err
	}
	items, err := s.reader.ItemsByID(ctx, []int64{id})
	if err != nil {
		return content.ListItem{}, err
	}
	if len(items) == 0 {
		return content.ListItem{}, errNoRandom
	}
	return items[0], nil
}

