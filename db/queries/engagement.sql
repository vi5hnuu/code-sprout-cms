-- name: ListStats :many
SELECT * FROM content_stats WHERE content_id = ANY (sqlc.arg(ids)::bigint[]);
