// Package maintenance runs periodic database upkeep from the worker.
package maintenance

import (
	"context"
	"fmt"
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
	ensure := func() {
		if err := EnsurePartitions(ctx, primary); err != nil && ctx.Err() == nil {
			slog.Error("ensure partitions", "err", err)
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

// EnsurePartitions creates any missing monthly partitions from this month to monthsAhead.
// Every table is attempted; the first failure is returned after the rest were tried.
func EnsurePartitions(ctx context.Context, primary *pgxpool.Pool) error {
	q := maintenancedb.New(primary)
	var first error
	for _, table := range monthlyPartitioned {
		if err := q.EnsureMonthlyPartitions(ctx, maintenancedb.EnsureMonthlyPartitionsParams{Parent: table, Months: monthsAhead + 1}); err != nil && first == nil {
			first = fmt.Errorf("%s: %w", table, err)
		}
	}
	return first
}
