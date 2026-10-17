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
