package maintenance

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/maintenance/maintenancedb"
)

// outboxRetentionDays: processed events are kept two months for debugging, then their monthly
// partitions are dropped whole by drop_processed_outbox_partitions (see migration 00007).
const outboxRetentionDays = 60

func PruneOutbox(ctx context.Context, primary *pgxpool.Pool) error {
	n, err := maintenancedb.New(primary).DropProcessedOutboxPartitions(ctx, outboxRetentionDays)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("outbox partitions dropped", "count", n)
	}
	return nil
}
