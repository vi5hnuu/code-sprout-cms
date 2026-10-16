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
