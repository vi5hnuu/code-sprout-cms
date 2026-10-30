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

// monthsAhead keeps a comfortable runway: a doubled 6-month window (industry baseline is 2–3×
// the maintenance interval; this upkeep runs daily and partitions are monthly) survives a
// weeks-long worker outage without insert failures. The old value of 3 cut it too close.
const monthsAhead = 6

// minRunwayMonths is the alert threshold. When any table has fewer than this many future
// partitions, we scream in the logs. The upkeep tick will heal it on its own, but an operator
// should see something is wrong (clock skew, grant missing, worker loop stuck, …).
const minRunwayMonths = 2

// KeepPartitionsAhead ensures the current and next months' partitions exist, at startup and
// then daily. Creation is idempotent, so concurrent workers are harmless. On startup it also
// warns if any table's runway has already dipped below minRunwayMonths — a signal that the
// previous worker was down long enough to matter.
func KeepPartitionsAhead(ctx context.Context, primary *pgxpool.Pool) {
	if err := CheckRunway(ctx, primary); err != nil {
		slog.Error("partition runway check", "err", err)
	}
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

// CheckRunway reports (via slog.Warn) any partitioned table with fewer than minRunwayMonths of
// future partitions. Called at startup, when it matters most: if the previous worker was down
// for a long time, this surfaces the risk before the next insert fails. The ensure tick that
// follows will fix the shortfall, but silent self-healing lets a real problem (a broken grant,
// a stuck cron on a sibling host) hide for months.
func CheckRunway(ctx context.Context, primary *pgxpool.Pool) error {
	q := maintenancedb.New(primary)
	var first error
	for _, table := range monthlyPartitioned {
		ahead, err := q.CountFuturePartitions(ctx, table)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("%s: %w", table, err)
			}
			continue
		}
		if ahead < minRunwayMonths {
			slog.Warn("partition runway low",
				"table", table, "months_ahead", ahead, "min", minRunwayMonths,
				"detail", "upkeep will heal; if this persists, something is wrong with the ensure job")
		}
	}
	return first
}
