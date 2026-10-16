-- name: EnsureMonthlyPartitions :exec
SELECT create_monthly_partitions(sqlc.arg(parent)::text::regclass, current_date, sqlc.arg(months)::int);
