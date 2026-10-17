-- ltree is mapped to string (sqlc.yaml); pgx exchanges unregistered types as text, so no
-- custom type registration is needed for the extension type.

-- name: GetCategory :one
SELECT * FROM category WHERE id = $1;

-- name: GetCategoryByPath :one
SELECT * FROM category WHERE path = sqlc.arg(path)::ltree;

-- name: ListRootCategories :many
SELECT * FROM category WHERE parent_id IS NULL
ORDER BY position, name;

-- name: ListChildCategories :many
SELECT * FROM category WHERE parent_id = $1
ORDER BY position, name;

-- Root-first chain of ancestors (excluding the node itself), for breadcrumbs.
-- name: ListCategoryAncestors :many
SELECT * FROM category
WHERE path @> sqlc.arg(path)::ltree AND path <> sqlc.arg(path)::ltree
ORDER BY nlevel(path);

-- name: InsertCategory :one
INSERT INTO category (parent_id, path, slug, name, description, position)
VALUES (sqlc.narg(parent_id), sqlc.arg(path)::ltree, sqlc.arg(slug), sqlc.arg(name), sqlc.narg(description), sqlc.arg(position))
RETURNING id;

-- name: UpdateCategoryDetails :exec
UPDATE category
SET name = sqlc.arg(name), description = sqlc.narg(description), seo = sqlc.arg(seo),
    position = sqlc.arg(position), updated_at = now()
WHERE id = sqlc.arg(id);

-- Re-roots a whole subtree in one statement: every path under old_path gets new_path as its
-- prefix. Used for both moves and slug renames (a rename is a move to the same parent).
-- The moved node itself is special-cased: subpath() rejects an offset equal to the path's
-- length, i.e. an empty remainder.
-- name: RewriteSubtreePaths :execrows
UPDATE category
SET path = CASE
        WHEN path = sqlc.arg(old_path)::ltree THEN sqlc.arg(new_path)::ltree
        ELSE sqlc.arg(new_path)::ltree || subpath(path, nlevel(sqlc.arg(old_path)::ltree))
    END,
    updated_at = now()
WHERE path <@ sqlc.arg(old_path)::ltree;

-- name: SetCategoryParentAndSlug :exec
UPDATE category SET parent_id = sqlc.narg(parent_id), slug = sqlc.arg(slug) WHERE id = sqlc.arg(id);

-- name: CategoryInUse :one
SELECT EXISTS (
    SELECT 1 FROM category c WHERE c.parent_id = sqlc.arg(id)::bigint
    UNION ALL
    SELECT 1 FROM content ct WHERE ct.primary_category_id = sqlc.arg(id)::bigint AND ct.deleted_at IS NULL
);

-- name: DeleteCategory :exec
DELETE FROM category WHERE id = $1;

-- name: GetTag :one
SELECT * FROM tag WHERE id = $1;

-- name: GetTagBySlug :one
SELECT * FROM tag WHERE kind = $1 AND slug = $2;

-- name: ListTags :many
SELECT * FROM tag
WHERE kind = sqlc.arg(kind)
  AND (sqlc.narg(after_name)::text IS NULL OR (name, id) > (sqlc.narg(after_name)::text, sqlc.narg(after_id)::bigint))
ORDER BY name, id
LIMIT sqlc.arg(row_limit);

-- Autocomplete: trigram similarity tolerates typos ("dynamc" → Dynamic Programming).
-- name: SearchTags :many
SELECT * FROM tag
WHERE name % sqlc.arg(q) AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
ORDER BY similarity(name, sqlc.arg(q)) DESC, name
LIMIT sqlc.arg(row_limit);

-- name: InsertTag :one
INSERT INTO tag (kind, slug, name, description) VALUES ($1, $2, $3, $4) RETURNING id;

-- name: UpdateTag :exec
UPDATE tag SET name = $2, description = $3 WHERE id = $1;

-- name: DeleteTag :exec
DELETE FROM tag WHERE id = $1;

-- Locks the node so concurrent moves/renames of the same subtree serialise.
-- name: GetCategoryForUpdate :one
SELECT * FROM category WHERE id = $1 FOR UPDATE;

-- Next sibling position for an append. IS NOT DISTINCT FROM handles roots (NULL parent); it
-- cannot use the (parent_id, position) index, which is fine for a rare admin write on a
-- table of at most a few thousand rows.
-- name: NextChildPosition :one
SELECT (COALESCE(max(position), 0) + 1)::double precision
FROM category WHERE parent_id IS NOT DISTINCT FROM sqlc.narg(parent_id)::bigint;

-- name: MaxSubtreeDepth :one
SELECT COALESCE(max(nlevel(path)), 0)::int FROM category WHERE path <@ sqlc.arg(path)::ltree;
