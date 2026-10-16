-- name: InsertEvent :exec
INSERT INTO outbox (topic, aggregate_id, payload) VALUES ($1, $2, $3);

-- Claims due events. SKIP LOCKED lets any number of workers share the queue without
-- double-processing; locks are held until the claiming transaction commits.
-- name: ClaimDueEvents :many
SELECT id, created_at, topic, aggregate_id, payload, attempts
FROM outbox
WHERE processed_at IS NULL AND available_at <= now()
ORDER BY available_at, id
LIMIT $1
FOR UPDATE SKIP LOCKED;

-- name: MarkProcessed :exec
UPDATE outbox SET processed_at = now()
WHERE created_at = $1 AND id = $2;

-- name: MarkFailed :exec
UPDATE outbox
SET attempts = attempts + 1, last_error = $3, available_at = $4
WHERE created_at = $1 AND id = $2;
