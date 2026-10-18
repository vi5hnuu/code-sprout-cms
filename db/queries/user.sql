-- name: FindUserIDBySub :one
SELECT id FROM app_user WHERE auth_sub = $1;

-- Concurrent first requests from the same user race here; DO NOTHING plus a re-read in the
-- service resolves it without a write on every request (an upsert would rewrite the row).
-- name: InsertUser :one
INSERT INTO app_user (auth_sub, display_name)
VALUES ($1, $2)
ON CONFLICT (auth_sub) DO NOTHING
RETURNING id;

-- name: GetUser :one
SELECT id, handle, display_name, bio, avatar_file_id, created_at
FROM app_user
WHERE id = $1;

-- name: ListRoleGrants :many
SELECT role, scope_category_id
FROM cms_role_grant
WHERE user_id = $1
ORDER BY role, scope_category_id NULLS FIRST;

-- name: ListPublicUsers :many
SELECT id, handle, display_name, bio, avatar_file_id FROM app_user WHERE id = ANY (sqlc.arg(ids)::bigint[]);

-- name: GetUserByHandle :one
SELECT id, handle, display_name, bio, avatar_file_id, created_at FROM app_user WHERE handle = $1;

-- name: UpdateProfile :one
UPDATE app_user
SET handle = sqlc.narg(handle), display_name = sqlc.arg(display_name), bio = sqlc.narg(bio),
    avatar_file_id = sqlc.narg(avatar_file_id), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, handle, display_name, bio, avatar_file_id, created_at;

-- Account erase: the row stays for references (authored pages, audit) but keeps nothing
-- personal. The auth subject is replaced so a returning sign-in provisions a fresh account.
-- name: AnonymizeUser :exec
UPDATE app_user
SET auth_sub = 'erased:' || id, handle = NULL, display_name = 'Former member', bio = NULL,
    avatar_file_id = NULL, deleted_at = now(), updated_at = now()
WHERE id = $1;
