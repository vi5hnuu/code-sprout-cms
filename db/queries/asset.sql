-- name: InsertFile :one
INSERT INTO file (owner_id, bucket, storage_key, mime, size_bytes, sha256, purpose)
VALUES (sqlc.arg(owner_id), sqlc.arg(bucket), sqlc.arg(storage_key), sqlc.arg(mime), sqlc.arg(size_bytes), sqlc.arg(sha256), sqlc.arg(purpose))
RETURNING *;

-- name: GetFile :one
SELECT * FROM file WHERE id = $1;

-- name: GetFileForUpdate :one
SELECT * FROM file WHERE id = $1 FOR UPDATE;

-- name: FindReadyDuplicate :one
SELECT * FROM file
WHERE owner_id = sqlc.arg(owner_id) AND purpose = sqlc.arg(purpose) AND sha256 = sqlc.arg(sha256) AND status = 'READY'
LIMIT 1;

-- name: MarkFileReady :one
UPDATE file
SET status = 'READY', width = sqlc.narg(width), height = sqlc.narg(height), lease_until = NULL, completed_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: MarkFileVerifying :one
UPDATE file SET status = 'VERIFYING', lease_until = NULL WHERE id = $1 RETURNING *;

-- name: MarkFileQuarantined :one
UPDATE file SET status = 'QUARANTINED', lease_until = NULL, completed_at = now() WHERE id = $1 RETURNING *;

-- Leases one file awaiting verification. The lease (not a held row lock) marks it taken, so
-- hashing a gigabyte never keeps a transaction open, and a crashed worker's file becomes
-- claimable again once its lease expires.
-- name: LeaseVerifyingFile :one
UPDATE file SET lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::int)
WHERE id = (
    SELECT f.id FROM file f
    WHERE f.status = 'VERIFYING' AND (f.lease_until IS NULL OR f.lease_until < now())
    ORDER BY f.id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ListFilesByID :many
SELECT * FROM file WHERE id = ANY (sqlc.arg(ids)::bigint[]);

-- One query per status, with the status written out, so each reads its partial index
-- (idx_file_pending, idx_file_quarantined) instead of scanning every file.
-- name: ListStalePendingFiles :many
SELECT * FROM file WHERE status = 'PENDING' AND created_at < sqlc.arg(before) ORDER BY created_at LIMIT sqlc.arg(row_limit);

-- name: ListStaleQuarantinedFiles :many
SELECT * FROM file WHERE status = 'QUARANTINED' AND created_at < sqlc.arg(before) ORDER BY created_at LIMIT sqlc.arg(row_limit);

-- Files let go of by the system (StatusDeleting), for garbage collection.
-- name: ListDeletingFiles :many
SELECT * FROM file WHERE status = 'DELETING' ORDER BY id LIMIT sqlc.arg(row_limit);

-- Account erase: the user's avatars stop being served and are collected.
-- name: MarkUserFilesDeleting :exec
UPDATE file SET status = 'DELETING' WHERE owner_id = sqlc.arg(owner_id) AND purpose = 'AVATAR';

-- name: DeleteFile :exec
DELETE FROM file WHERE id = $1;

-- name: InsertReadyFile :one
INSERT INTO file (owner_id, bucket, storage_key, mime, size_bytes, sha256, status, purpose, completed_at)
VALUES (NULL, sqlc.arg(bucket), sqlc.arg(storage_key), sqlc.arg(mime), sqlc.arg(size_bytes), sqlc.arg(sha256), 'READY', sqlc.arg(purpose), now())
RETURNING id;
