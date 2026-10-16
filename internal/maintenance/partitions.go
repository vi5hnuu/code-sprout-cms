// Package maintenance runs periodic database upkeep from the worker.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/maintenance/maintenancedb"
)

// monthlyPartitioned lists every RANGE-by-month table. A new partitioned table must be added
// here, or inserts fail once the months created by its migration run out.
var monthlyPartitioned = []string{"outbox", "audit_log", "content_daily_stats"}

// monthsAhead keeps a comfortable runway: even if the worker is down for weeks, writes keep
// landing in existing partitions.
const monthsAhead = 3

// KeepPartitionsAhead ensures the current and next months' partitions exist, at startup and
// then daily. Creation is idempotent, so concurrent workers are harmless.
func KeepPartitionsAhead(ctx context.Context, primary *pgxpool.Pool) {
	q := maintenancedb.New(primary)
	ensure := func() {
		for _, table := range monthlyPartitioned {
			if err := q.EnsureMonthlyPartitions(ctx, table, monthsAhead+1); err != nil && ctx.Err() == nil {
				slog.Error("ensure partitions", "table", table, "err", err)
			}
		}
	}
	ensure()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			ensure()
		}
	}
}
