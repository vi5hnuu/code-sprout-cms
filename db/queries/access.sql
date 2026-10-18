-- True when the user holds any of `roles` either globally or on a category whose subtree
-- contains `category_id`. A NULL category_id (no category yet) only matches global grants.
-- name: HasRoleFor :one
SELECT EXISTS (
    SELECT 1
    FROM cms_role_grant g
    LEFT JOIN category scope ON scope.id = g.scope_category_id
    WHERE g.user_id = sqlc.arg(user_id)
      AND g.role = ANY (sqlc.arg(roles)::text[])
      AND (
          g.scope_category_id IS NULL
          OR EXISTS (
              SELECT 1 FROM category c
              WHERE c.id = sqlc.narg(category_id)::bigint AND c.path <@ scope.path
          )
      )
);

-- Role administration, newest grant first; optionally one user's grants.
-- name: ListGrants :many
SELECT id, user_id, role, scope_category_id, granted_by, created_at
FROM cms_role_grant
WHERE (sqlc.narg(user_id)::bigint IS NULL OR user_id = sqlc.narg(user_id))
  AND (sqlc.narg(after_id)::bigint IS NULL OR id < sqlc.narg(after_id))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: InsertGrant :one
INSERT INTO cms_role_grant (user_id, role, scope_category_id, granted_by)
VALUES (sqlc.arg(user_id), sqlc.arg(role), sqlc.narg(scope_category_id), sqlc.arg(granted_by))
RETURNING id, user_id, role, scope_category_id, granted_by, created_at;

-- name: DeleteGrant :one
DELETE FROM cms_role_grant WHERE id = $1
RETURNING id, user_id, role, scope_category_id, granted_by, created_at;

-- name: DeleteUserGrants :exec
DELETE FROM cms_role_grant WHERE user_id = $1;
