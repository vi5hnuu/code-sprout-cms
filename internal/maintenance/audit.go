package maintenance

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/maintenance/maintenancedb"
)

// auditRetentionDays: how long to keep the admin-action trail. Two years covers typical
// regulatory asks (DPDPA, SOC-2 Type II). Each monthly partition is dropped whole once its
// upper bound falls outside the window — instant, no vacuum debt. If a specific audit entry
// has to live longer, export it from Postgres into cold storage before this cron drops it.
const auditRetentionDays = 365 * 2

// PruneAuditLog drops expired audit_log partitions. Idempotent; safe to call daily.
func PruneAuditLog(ctx context.Context, primary *pgxpool.Pool) error {
	n, err := maintenancedb.New(primary).DropOldAuditPartitions(ctx, auditRetentionDays)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Info("audit_log partitions dropped", "count", n)
	}
	return nil
}
