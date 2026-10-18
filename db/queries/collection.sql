-- name: InsertCollection :one
INSERT INTO collection (kind, slug, title, description, owner_id, visibility)
VALUES (sqlc.arg(kind), sqlc.arg(slug), sqlc.arg(title), sqlc.narg(description), sqlc.arg(owner_id), sqlc.arg(visibility))
RETURNING *;

-- name: GetCollection :one
SELECT * FROM collection WHERE id = $1;

-- name: GetCollectionForUpdate :one
SELECT * FROM collection WHERE id = $1 FOR UPDATE;

-- name: GetCollectionBySlug :one
SELECT * FROM collection WHERE kind = $1 AND slug = $2;

-- name: UpdateCollection :one
UPDATE collection
SET title = sqlc.arg(title), description = sqlc.narg(description), visibility = sqlc.arg(visibility), updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteCollection :exec
DELETE FROM collection WHERE id = $1;

-- name: ListOwnerCollections :many
SELECT c.*, (SELECT count(*) FROM collection_item i WHERE i.collection_id = c.id)::int AS item_count
FROM collection c WHERE c.owner_id = $1 AND c.kind = 'LIST'
ORDER BY c.updated_at DESC LIMIT 200;

-- name: CountItems :one
SELECT count(*)::int FROM collection_item WHERE collection_id = $1;

-- Appends after the last item.
-- name: AddItem :execrows
INSERT INTO collection_item (collection_id, content_id, position)
SELECT sqlc.arg(collection_id), sqlc.arg(content_id), COALESCE(max(position), 0) + 1
FROM collection_item WHERE collection_id = sqlc.arg(collection_id)
ON CONFLICT DO NOTHING;

-- name: RemoveItem :execrows
DELETE FROM collection_item WHERE collection_id = $1 AND content_id = $2;

-- name: ClearItems :exec
DELETE FROM collection_item WHERE collection_id = $1;

-- name: InsertItemAt :exec
INSERT INTO collection_item (collection_id, content_id, position) VALUES ($1, $2, $3);

-- name: TouchCollection :exec
UPDATE collection SET updated_at = now() WHERE id = $1;

-- name: ListItems :many
SELECT content_id, position FROM collection_item
WHERE collection_id = sqlc.arg(collection_id)
  AND (sqlc.narg(after_position)::double precision IS NULL OR position > sqlc.narg(after_position)::double precision)
ORDER BY position
LIMIT sqlc.arg(row_limit);

-- name: CollectionsContaining :many
SELECT c.id FROM collection c
JOIN collection_item i ON i.collection_id = c.id
WHERE c.owner_id = $1 AND c.kind = 'LIST' AND i.content_id = $2;

-- Editorial collections of one kind for the studio, newest first (any owner).
-- Public study plans, series or courses for their index pages, newest first.
-- name: ListPublicEditorialCollections :many
SELECT c.*, (SELECT count(*) FROM collection_item i WHERE i.collection_id = c.id)::int AS item_count
FROM collection c
WHERE c.kind = sqlc.arg(kind) AND c.visibility = 'PUBLIC'
  AND (sqlc.narg(after_id)::bigint IS NULL OR c.id < sqlc.narg(after_id))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListEditorialCollections :many
SELECT c.*, (SELECT count(*) FROM collection_item i WHERE i.collection_id = c.id)::int AS item_count
FROM collection c
WHERE c.kind = sqlc.arg(kind)
  AND (sqlc.narg(after_id)::bigint IS NULL OR c.id < sqlc.narg(after_id))
ORDER BY c.id DESC
LIMIT sqlc.arg(row_limit);
