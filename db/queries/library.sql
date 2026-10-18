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
