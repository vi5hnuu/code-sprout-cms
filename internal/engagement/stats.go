// Package engagement owns how readers interact with content: likes, bookmarks, comments,
// shares and views, and the counters that summarise them.
package engagement

import (
	"context"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/user"
)

// Stats are the public counters for one item. They are maintained incrementally (never
// COUNT(*)), so they can lag the underlying rows by a few seconds for views.
type Stats struct {
	Views     int64 `json:"views"`
	Likes     int64 `json:"likes"`
	Comments  int64 `json:"comments"`
	Bookmarks int64 `json:"bookmarks"`
	Shares    int64 `json:"shares"`
}

// ContentGate tells which items are live (implemented by content.Reader). Engagement only
// ever touches live content, and depends on content through this one question.
type ContentGate interface {
	LiveIDs(ctx context.Context, ids []int64) ([]int64, error)
}

// Authors resolves comment authors (implemented by user.Service).
type Authors interface {
	PublicProfiles(ctx context.Context, ids []int64) (map[int64]user.PublicUser, error)
}

type Service struct {
	db      *db.DB
	rdb     redis.UniversalClient
	content ContentGate
	authors Authors
	policy  *access.Policy
}

func NewService(d *db.DB, rdb redis.UniversalClient, content ContentGate, authors Authors, policy *access.Policy) *Service {
	return &Service{db: d, rdb: rdb, content: content, authors: authors, policy: policy}
}

// requireLive fails with 404 unless the item is live: engagement on drafts or removed pages
// is never recorded.
func (s *Service) requireLive(ctx context.Context, contentID int64) error {
	live, err := s.content.LiveIDs(ctx, []int64{contentID})
	if err != nil {
		return err
	}
	if len(live) == 0 {
		return apperr.NotFound("content.not_found", "No such content.")
	}
	return nil
}

// deltas are counter changes applied with the row change that causes them.
type deltas struct{ views, likes, comments, bookmarks, shares int64 }

func applyDeltas(ctx context.Context, q *engagementdb.Queries, contentID int64, d deltas) error {
	if err := q.ApplyStatDeltas(ctx, engagementdb.ApplyStatDeltasParams{
		ContentID: contentID, Views: d.views, Likes: d.likes, Comments: d.comments, Bookmarks: d.bookmarks, Shares: d.shares,
	}); err != nil {
		return err
	}
	pos := func(v int64) int64 { return max(v, 0) }
	return q.AddDailyStats(ctx, engagementdb.AddDailyStatsParams{
		ContentID: contentID, Views: pos(d.views), Likes: int32(pos(d.likes)), Comments: int32(pos(d.comments)),
		Bookmarks: int32(pos(d.bookmarks)), Shares: int32(pos(d.shares)),
	})
}

// StatsReader serves counters to other domains. It depends only on the database (not on
// content), so content can read counters without an import or construction cycle.
type StatsReader struct{ db *db.DB }

func NewStatsReader(d *db.DB) *StatsReader { return &StatsReader{db: d} }

// StatsFor batch-loads counters; items with no stats row read as zero.
func (s *StatsReader) StatsFor(ctx context.Context, ids []int64) (map[int64]Stats, error) {
	out := make(map[int64]Stats, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := engagementdb.New(s.db.Replica).ListStats(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ContentID] = Stats{Views: r.Views, Likes: r.Likes, Comments: r.Comments, Bookmarks: r.Bookmarks, Shares: r.Shares}
	}
	return out, nil
}
