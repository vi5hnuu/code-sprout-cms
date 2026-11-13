-- name: UpsertNote :one
INSERT INTO user_note (user_id, content_id, body_md, updated_at) VALUES ($1, $2, $3, now())
ON CONFLICT (user_id, content_id) DO UPDATE SET body_md = EXCLUDED.body_md, updated_at = now()
RETURNING *;

-- name: GetNote :one
SELECT * FROM user_note WHERE user_id = $1 AND content_id = $2;

-- name: DeleteNote :exec
DELETE FROM user_note WHERE user_id = $1 AND content_id = $2;

-- name: ListNotes :many
SELECT content_id, updated_at FROM user_note
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(after_updated)::timestamptz IS NULL
       OR (updated_at, content_id) < (sqlc.narg(after_updated)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY updated_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: DeleteUserNotes :exec
DELETE FROM user_note WHERE user_id = $1;

-- ── Reading progress ────────────────────────────────────────────────────────

-- The latest position wins; finished_at is set once and kept.
-- name: UpsertReading :one
INSERT INTO reading_progress (user_id, content_id, heading_id, percent, updated_at, finished_at)
VALUES (sqlc.arg(user_id), sqlc.arg(content_id), sqlc.narg(heading_id), sqlc.arg(percent), now(),
        CASE WHEN sqlc.arg(percent)::smallint >= sqlc.arg(finished_at_percent)::smallint THEN now() END)
ON CONFLICT (user_id, content_id) DO UPDATE
SET heading_id  = EXCLUDED.heading_id,
    percent     = EXCLUDED.percent,
    updated_at  = now(),
    finished_at = COALESCE(reading_progress.finished_at, EXCLUDED.finished_at)
RETURNING *;

-- name: DeleteReading :exec
DELETE FROM reading_progress WHERE user_id = $1 AND content_id = $2;

-- name: ListReadingInProgress :many
SELECT * FROM reading_progress
WHERE user_id = sqlc.arg(user_id) AND finished_at IS NULL
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (updated_at, content_id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY updated_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListReadingFinished :many
SELECT * FROM reading_progress
WHERE user_id = sqlc.arg(user_id) AND finished_at IS NOT NULL
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (finished_at, content_id) < (sqlc.narg(after_at)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY finished_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: ReadingStates :many
SELECT * FROM reading_progress WHERE user_id = sqlc.arg(user_id) AND content_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: DeleteUserReading :exec
DELETE FROM reading_progress WHERE user_id = $1;

-- Retention: positions untouched for a year go, a batch at a time (the caller loops).
-- name: PruneReading :execrows
DELETE FROM reading_progress rp
WHERE (rp.user_id, rp.content_id) IN (
    SELECT old.user_id, old.content_id FROM reading_progress old
    WHERE old.updated_at < sqlc.arg(before)::timestamptz
    LIMIT sqlc.arg(batch)::int
);
