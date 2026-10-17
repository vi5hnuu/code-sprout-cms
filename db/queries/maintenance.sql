-- name: EnsureMonthlyPartitions :exec
SELECT create_monthly_partitions(sqlc.arg(parent)::text::regclass, current_date, sqlc.arg(months)::int);


-- name: DropProcessedOutboxPartitions :one
SELECT drop_processed_outbox_partitions(sqlc.arg(retention_days)::int)::int;
