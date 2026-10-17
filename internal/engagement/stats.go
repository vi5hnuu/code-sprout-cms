// Package engagement owns how readers interact with content: likes, bookmarks, comments,
// shares and views, and the counters that summarise them.
package engagement

import (
	"context"

	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/platform/db"
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

type Service struct {
	db *db.DB
}

func NewService(d *db.DB) *Service { return &Service{db: d} }

// StatsFor batch-loads counters; items with no stats row read as zero.
func (s *Service) StatsFor(ctx context.Context, ids []int64) (map[int64]Stats, error) {
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
