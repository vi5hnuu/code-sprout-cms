-- name: InsertEvent :exec
INSERT INTO outbox (topic, aggregate_id, payload) VALUES ($1, $2, $3);

-- Leases a batch of due events: pushing available_at forward hides them from other workers
-- for the lease, so handlers run outside any transaction. If the worker dies, the lease
-- lapses and the events are delivered again (at-least-once).
-- name: LeaseDueEvents :many
UPDATE outbox o
SET available_at = now() + make_interval(secs => sqlc.arg(lease_seconds)::int)
FROM (
    SELECT created_at, id FROM outbox
    WHERE processed_at IS NULL AND available_at <= now()
    ORDER BY available_at, id
    LIMIT sqlc.arg(batch)
    FOR UPDATE SKIP LOCKED
) due
WHERE o.created_at = due.created_at AND o.id = due.id
RETURNING o.id, o.created_at, o.topic, o.aggregate_id, o.payload, o.attempts;

-- name: MarkProcessed :exec
UPDATE outbox SET processed_at = now()
WHERE created_at = $1 AND id = $2;

-- name: MarkFailed :exec
UPDATE outbox
SET attempts = attempts + 1, last_error = $3, available_at = $4
WHERE created_at = $1 AND id = $2;
