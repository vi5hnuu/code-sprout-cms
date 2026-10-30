-- name: ListStats :many
SELECT * FROM content_stats WHERE content_id = ANY (sqlc.arg(ids)::bigint[]);

-- Counters move by deltas in the same transaction as the row that justifies them, so they
-- never need COUNT(*) on the read path.
-- name: ApplyStatDeltas :exec
UPDATE content_stats
SET views = views + sqlc.arg(views), likes = likes + sqlc.arg(likes), comments = comments + sqlc.arg(comments),
    bookmarks = bookmarks + sqlc.arg(bookmarks), shares = shares + sqlc.arg(shares), updated_at = now()
WHERE content_id = sqlc.arg(content_id);

-- Daily rollups count events that happened that day (additions only).
-- name: AddDailyStats :exec
INSERT INTO content_daily_stats (content_id, day, views, likes, comments, bookmarks, shares)
VALUES (sqlc.arg(content_id), current_date, sqlc.arg(views), sqlc.arg(likes), sqlc.arg(comments), sqlc.arg(bookmarks), sqlc.arg(shares))
ON CONFLICT (content_id, day) DO UPDATE
SET views = content_daily_stats.views + EXCLUDED.views, likes = content_daily_stats.likes + EXCLUDED.likes,
    comments = content_daily_stats.comments + EXCLUDED.comments, bookmarks = content_daily_stats.bookmarks + EXCLUDED.bookmarks,
    shares = content_daily_stats.shares + EXCLUDED.shares;

-- ── Reactions ───────────────────────────────────────────────────────────────

-- name: InsertReaction :execrows
INSERT INTO reaction (content_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: DeleteReaction :execrows
DELETE FROM reaction WHERE content_id = $1 AND user_id = $2;

-- name: ReactedIDs :many
SELECT content_id FROM reaction WHERE user_id = sqlc.arg(user_id) AND content_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: ListUserReactions :many
SELECT content_id, created_at FROM reaction
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, content_id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- ── Bookmarks ───────────────────────────────────────────────────────────────

-- 1 when newly bookmarked (counters move), 0 when it already was (then MoveBookmark).
-- name: InsertBookmark :execrows
INSERT INTO bookmark (user_id, content_id, list_id) VALUES (sqlc.arg(user_id), sqlc.arg(content_id), sqlc.narg(list_id))
ON CONFLICT (user_id, content_id) DO NOTHING;

-- name: MoveBookmark :exec
UPDATE bookmark SET list_id = sqlc.narg(list_id) WHERE user_id = sqlc.arg(user_id) AND content_id = sqlc.arg(content_id);

-- name: DeleteBookmark :execrows
DELETE FROM bookmark WHERE user_id = $1 AND content_id = $2;

-- Reads from user_stats (migration 00018) — a denormalised counter maintained by the
-- bookmark_user_stats_sync trigger. O(1) instead of O(rows). COALESCE handles the rare case
-- of a user whose first-ever bookmark hasn't yet inserted (no row in user_stats yet).
-- name: CountUserBookmarks :one
SELECT COALESCE((SELECT bookmark_count::bigint FROM user_stats WHERE user_id = $1), 0::bigint);

-- name: BookmarkStates :many
SELECT content_id, list_id FROM bookmark WHERE user_id = sqlc.arg(user_id) AND content_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: ListUserBookmarks :many
SELECT content_id, list_id, created_at FROM bookmark
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(list_id)::bigint IS NULL OR list_id = sqlc.narg(list_id)::bigint)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, content_id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: InsertBookmarkList :one
INSERT INTO bookmark_list (user_id, name) VALUES ($1, $2) RETURNING *;

-- name: ListBookmarkLists :many
SELECT * FROM bookmark_list WHERE user_id = $1 ORDER BY name LIMIT 200;

-- name: GetBookmarkList :one
SELECT * FROM bookmark_list WHERE id = $1 AND user_id = $2;

-- name: DeleteBookmarkList :execrows
DELETE FROM bookmark_list WHERE id = $1 AND user_id = $2;

-- name: ClearBookmarkList :exec
UPDATE bookmark SET list_id = NULL WHERE user_id = $1 AND list_id = $2;

-- ── Comments ────────────────────────────────────────────────────────────────

-- name: InsertComment :one
INSERT INTO comment (content_id, root_id, parent_id, author_id, body_md)
VALUES (sqlc.arg(content_id), sqlc.narg(root_id), sqlc.narg(parent_id), sqlc.arg(author_id), sqlc.arg(body_md))
RETURNING *;

-- name: GetComment :one
SELECT * FROM comment WHERE content_id = $1 AND id = $2;

-- name: GetCommentForUpdate :one
SELECT * FROM comment WHERE content_id = $1 AND id = $2 FOR UPDATE;

-- name: BumpReplyCount :exec
UPDATE comment SET reply_count = reply_count + sqlc.arg(delta) WHERE content_id = sqlc.arg(content_id) AND id = sqlc.arg(id);

-- name: EditComment :one
UPDATE comment SET body_md = $3, edited_at = now() WHERE content_id = $1 AND id = $2 RETURNING *;

-- name: SetCommentStatus :exec
UPDATE comment SET status = $3 WHERE content_id = $1 AND id = $2;

-- Deleted roots stay (as "[deleted]") while they have visible replies, so threads keep context.
-- name: ListRootComments :many
SELECT * FROM comment
WHERE content_id = sqlc.arg(content_id) AND root_id IS NULL
  AND (status = 'VISIBLE' OR (status = 'DELETED' AND reply_count > 0))
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListReplies :many
SELECT * FROM comment
WHERE content_id = sqlc.arg(content_id) AND root_id = sqlc.arg(root_id) AND status = 'VISIBLE'
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at, id
LIMIT sqlc.arg(row_limit);

-- name: InsertCommentReport :execrows
INSERT INTO comment_report (content_id, comment_id, reporter_id, reason, note)
VALUES (sqlc.arg(content_id), sqlc.arg(comment_id), sqlc.arg(reporter_id), sqlc.arg(reason), sqlc.narg(note))
ON CONFLICT (comment_id, reporter_id) DO NOTHING;

-- name: CountOpenReports :one
SELECT count(*) FROM comment_report WHERE comment_id = $1 AND resolved_at IS NULL;

-- name: ListOpenReports :many
SELECT r.*, c.body_md, c.author_id, c.status AS comment_status
FROM comment_report r
JOIN comment c ON c.content_id = r.content_id AND c.id = r.comment_id
WHERE r.resolved_at IS NULL
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (r.created_at, r.id) > (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY r.created_at, r.id
LIMIT sqlc.arg(row_limit);

-- name: ResolveReports :exec
UPDATE comment_report SET resolved_at = now(), resolved_by = sqlc.arg(resolver)
WHERE comment_id = sqlc.arg(comment_id) AND resolved_at IS NULL;

-- Recomputes likes and visible-comment counts for a batch of items from the rows themselves,
-- repairing any drift in the incrementally maintained counters. Returns the batch's ids.
-- name: ReconcileStats :many
UPDATE content_stats s
SET likes = (SELECT count(*) FROM reaction r WHERE r.content_id = s.content_id),
    comments = (SELECT count(*) FROM comment c WHERE c.content_id = s.content_id AND c.status = 'VISIBLE'),
    updated_at = now()
WHERE s.content_id IN (
    SELECT b.content_id FROM content_stats b WHERE b.content_id > sqlc.arg(after_id) ORDER BY b.content_id LIMIT sqlc.arg(row_limit)
)
RETURNING s.content_id;

-- Account erase. Each returns the pages it touched so counters and caches can follow.

-- name: DeleteUserReactions :many
DELETE FROM reaction WHERE user_id = $1 RETURNING content_id;

-- name: DeleteUserBookmarks :many
DELETE FROM bookmark WHERE user_id = $1 RETURNING content_id;

-- name: DeleteUserBookmarkLists :exec
DELETE FROM bookmark_list WHERE user_id = $1;

-- Every comment by the user loses its text (deleted ones too: deleting only hid them). The old
-- status says which were visible, so comment and reply counts drop by exactly those.
-- name: EraseUserComments :many
WITH old AS (
    SELECT cm.content_id, cm.id, cm.root_id, cm.status FROM comment cm WHERE cm.author_id = $1 FOR UPDATE
)
UPDATE comment c
SET status = 'DELETED', body_md = '[deleted]', edited_at = NULL
FROM old
WHERE c.content_id = old.content_id AND c.id = old.id
RETURNING old.content_id, old.root_id, old.status AS old_status;
