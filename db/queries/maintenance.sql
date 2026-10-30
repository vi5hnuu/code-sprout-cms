-- name: EnsureMonthlyPartitions :exec
SELECT create_monthly_partitions(sqlc.arg(parent)::text::regclass, current_date, sqlc.arg(months)::int);


-- name: DropProcessedOutboxPartitions :one
SELECT drop_processed_outbox_partitions(sqlc.arg(retention_days)::int)::int;


-- name: DropOldAuditPartitions :one
SELECT drop_old_audit_partitions(sqlc.arg(retention_days)::int)::int;


-- Count the monthly partitions of `parent` that cover the current month or any future month.
-- Partitions created by `create_monthly_partitions` are named `<parent>_yYYYYmMM`, so parsing
-- the suffix is cheap and avoids relying on fragile pg_get_expr parsing of partition bounds.
-- The startup runway check (internal/maintenance/partitions.go) uses this to alert when a
-- table's future-month runway dips below the agreed minimum.
-- name: CountFuturePartitions :one
SELECT count(*)::int FROM pg_inherits i
JOIN pg_class child  ON child.oid  = i.inhrelid
JOIN pg_class parent ON parent.oid = i.inhparent
WHERE parent.relname = sqlc.arg(parent)::text
  AND child.relname ~ ('^' || parent.relname || '_y[0-9]{4}m[0-9]{2}$')
  AND to_date(substring(child.relname FROM '_y([0-9]{4}m[0-9]{2})$'), 'YYYY"m"MM')
      >= date_trunc('month', current_date);
