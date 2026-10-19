-- ── Sources (one per synced channel) ──

-- name: UpsertSource :one
INSERT INTO video_source (provider, channel_id, owner_user_id, excluded_playlists)
VALUES ('YOUTUBE', sqlc.arg(channel_id), sqlc.arg(owner_user_id), sqlc.arg(excluded_playlists)::text[])
ON CONFLICT (provider, channel_id) DO UPDATE
SET owner_user_id = EXCLUDED.owner_user_id, excluded_playlists = EXCLUDED.excluded_playlists, enabled = true
RETURNING *;

-- Channels dropped from the configuration stop syncing (their videos stay until removed by hand).
-- name: DisableOtherSources :exec
UPDATE video_source SET enabled = false
WHERE enabled AND provider = 'YOUTUBE' AND NOT (channel_id = ANY (sqlc.arg(channel_ids)::text[]));

-- name: SetUploadsPlaylist :exec
UPDATE video_source SET uploads_playlist_id = sqlc.arg(playlist_id) WHERE id = sqlc.arg(id);

-- Starts a full pass if none is running and returns when the running one began.
-- name: BeginReconcile :one
UPDATE video_source SET reconcile_started_at = COALESCE(reconcile_started_at, now())
WHERE id = sqlc.arg(id)
RETURNING reconcile_started_at::timestamptz AS started_at, reconcile_page;

-- name: SaveReconcilePage :exec
UPDATE video_source SET reconcile_page = sqlc.narg(page) WHERE id = sqlc.arg(id);

-- name: FinishReconcile :exec
UPDATE video_source SET reconcile_page = NULL, reconcile_started_at = NULL, reconciled_at = now() WHERE id = sqlc.arg(id);

-- Spends units of today's quota (UTC day) if they fit under the limit; returns whether it did.
-- name: SpendQuota :one
UPDATE video_source
SET quota_used = CASE WHEN quota_day = (now() AT TIME ZONE 'utc')::date THEN quota_used ELSE 0 END + sqlc.arg(units)::int,
    quota_day = (now() AT TIME ZONE 'utc')::date
WHERE id = sqlc.arg(id)
  AND CASE WHEN quota_day = (now() AT TIME ZONE 'utc')::date THEN quota_used ELSE 0 END + sqlc.arg(units)::int <= sqlc.arg(daily_limit)::int
RETURNING quota_used;

-- One worker syncs a source at a time (session advisory lock, released after the run).
-- name: TryLockSource :one
SELECT pg_try_advisory_lock(hashtext('video_source'), sqlc.arg(id)::int);

-- name: UnlockSource :exec
SELECT pg_advisory_unlock(hashtext('video_source'), sqlc.arg(id)::int);

-- name: RecordSync :exec
UPDATE video_source SET last_synced_at = now(), last_error = sqlc.narg(last_error) WHERE id = sqlc.arg(id);

-- ── Videos ──

-- name: GetExtForUpdate :one
SELECT * FROM video_ext WHERE provider = 'YOUTUBE' AND external_id = sqlc.arg(external_id) FOR UPDATE;

-- name: GetExt :one
SELECT * FROM video_ext WHERE content_id = $1;

-- name: InsertExt :exec
INSERT INTO video_ext (content_id, source_id, external_id, duration_s, published_at, thumbnail_url, thumbnail_w, thumbnail_h, is_short, chapters, synced_rev_no, source_hash)
VALUES (sqlc.arg(content_id), sqlc.arg(source_id), sqlc.arg(external_id), sqlc.arg(duration_s), sqlc.arg(published_at),
        sqlc.arg(thumbnail_url), sqlc.narg(thumbnail_w), sqlc.narg(thumbnail_h), sqlc.arg(is_short), sqlc.arg(chapters),
        sqlc.narg(synced_rev_no), sqlc.arg(source_hash));

-- name: UpdateExtMedia :exec
UPDATE video_ext
SET duration_s = sqlc.arg(duration_s), published_at = sqlc.arg(published_at), thumbnail_url = sqlc.arg(thumbnail_url),
    thumbnail_w = sqlc.narg(thumbnail_w), thumbnail_h = sqlc.narg(thumbnail_h), is_short = sqlc.arg(is_short),
    chapters = sqlc.arg(chapters), synced_at = now(), seen_at = now()
WHERE content_id = sqlc.arg(content_id);

-- The sync published this revision from this source text (and the item is back if it was gone).
-- name: SetSyncedRev :exec
UPDATE video_ext SET synced_rev_no = sqlc.arg(rev_no), source_hash = sqlc.arg(source_hash), removed_at = NULL
WHERE content_id = sqlc.arg(content_id);

-- name: MarkRemoved :exec
UPDATE video_ext SET removed_at = now() WHERE content_id = sqlc.arg(content_id);

-- Shorts are also the short videos in a playlist named as shorts (YouTube allows up to 3 min).
-- name: MarkShorts :exec
UPDATE video_ext SET is_short = true
WHERE provider = 'YOUTUBE' AND external_id = ANY (sqlc.arg(external_ids)::text[]) AND duration_s <= sqlc.arg(max_seconds) AND NOT is_short;

-- name: MarkSeen :exec
UPDATE video_ext SET seen_at = now() WHERE provider = 'YOUTUBE' AND external_id = ANY (sqlc.arg(external_ids)::text[]);

-- Videos of a source a full pass did not see (gone from the channel). Reads idx_video_ext_source.
-- name: ListUnseen :many
SELECT content_id FROM video_ext
WHERE source_id = sqlc.arg(source_id) AND seen_at < sqlc.arg(before)
ORDER BY seen_at
LIMIT sqlc.arg(row_limit);

-- name: ContentIDsByExternal :many
SELECT external_id, content_id FROM video_ext WHERE provider = 'YOUTUBE' AND external_id = ANY (sqlc.arg(external_ids)::text[]);

-- name: MediaByIDs :many
SELECT content_id, thumbnail_url, duration_s, is_short FROM video_ext WHERE content_id = ANY (sqlc.arg(ids)::bigint[]);

-- Newest first, optionally one format; keyset on (published_at, content_id).
-- name: ListVideoIDs :many
SELECT content_id, published_at FROM video_ext
WHERE (sqlc.narg(is_short)::bool IS NULL OR is_short = sqlc.narg(is_short)::bool)
  AND (sqlc.narg(after_published)::timestamptz IS NULL
       OR (published_at, content_id) < (sqlc.narg(after_published)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY published_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: MarkSeenContent :exec
UPDATE video_ext SET seen_at = now() WHERE content_id = sqlc.arg(content_id);
