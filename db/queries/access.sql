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
