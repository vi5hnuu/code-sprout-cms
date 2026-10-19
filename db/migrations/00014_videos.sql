-- Videos: the owner's YouTube uploads as content of type VIDEO.
--
-- A video is an ordinary content item (pages, search, sitemaps, likes, comments and lists come
-- with that) plus one video_ext row. YouTube stays the system of record for the media; only
-- metadata lives here. A worker keeps it in step with each configured channel (video_source).

-- +goose Up
ALTER TABLE content DROP CONSTRAINT content_type_check;
ALTER TABLE content ADD CONSTRAINT content_type_check
    CHECK (type IN ('ARTICLE', 'BLOG', 'PROBLEM', 'TOPIC_PAGE', 'QUIZ', 'VIDEO'));

-- VIDEO: a problem's video solution (written by the problem's publish hook from the editor's
-- choice, or by the video sync from an exact title match when no editor has chosen).
ALTER TABLE content_link DROP CONSTRAINT content_link_kind_check;
ALTER TABLE content_link ADD CONSTRAINT content_link_kind_check
    CHECK (kind IN ('PRACTICE', 'RELATED', 'PREREQUISITE', 'EDITORIAL', 'VIDEO'));

-- One row per synced channel. Several channels (other authors, other regions) are rows, not code.
CREATE TABLE video_source (
    id                  bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider            text        NOT NULL DEFAULT 'YOUTUBE' CHECK (provider IN ('YOUTUBE')),
    channel_id          text        NOT NULL,
    -- The account videos are published as (author, audit actor).
    owner_user_id       bigint      NOT NULL REFERENCES app_user (id),
    uploads_playlist_id text,
    -- Playlists never imported as series (e.g. non-programming ones).
    excluded_playlists  text[]      NOT NULL DEFAULT '{}',
    enabled             bool        NOT NULL DEFAULT true,
    -- Resumable full reconcile: the uploads page token to continue from (NULL = start over).
    reconcile_page      text,
    -- When the current full pass began: videos not seen since then are gone at its end.
    reconcile_started_at timestamptz,
    reconciled_at       timestamptz,
    last_synced_at      timestamptz,
    last_error          text,
    -- YouTube's daily quota, shared by every worker (the counter lives here, not in memory).
    quota_day           date,
    quota_used          int         NOT NULL DEFAULT 0,
    UNIQUE (provider, channel_id)
);

-- Unpartitioned on purpose: millions of narrow rows are trivial for a B-tree, and the upsert by
-- (provider, external_id) needs one global unique index. If it ever must split, content_id is
-- the key, like every per-content table.
CREATE TABLE video_ext (
    content_id    bigint      PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    source_id     bigint      NOT NULL REFERENCES video_source (id),
    provider      text        NOT NULL DEFAULT 'YOUTUBE',
    external_id   text        NOT NULL,
    duration_s    int         NOT NULL CHECK (duration_s >= 0),
    published_at  timestamptz NOT NULL,
    thumbnail_url text        NOT NULL,
    thumbnail_w   int,
    thumbnail_h   int,
    is_short      bool        NOT NULL DEFAULT false,
    -- [{"t": seconds, "label": "Intro"}] from "0:00 Intro" lines in the description.
    chapters      jsonb       NOT NULL DEFAULT '[]',
    -- The published revision the sync last wrote. While the item is still at it, no editor has
    -- changed the text, so the sync may refresh title and notes; afterwards editors own them.
    synced_rev_no int,
    synced_at     timestamptz NOT NULL DEFAULT now(),
    -- Last time a sync found it on the channel; a full pass archives what it no longer sees.
    seen_at       timestamptz NOT NULL DEFAULT now(),
    -- Hash of the source title and description: a change means the source text was edited.
    source_hash   bytea,
    -- Set when the sync took the item down because it left the channel; only such items are
    -- republished if they come back (an editor's own archive is never undone by the sync).
    removed_at    timestamptz,
    UNIQUE (provider, external_id)
);
-- Listing rails: all videos newest first, and by format (regular / shorts).
CREATE INDEX idx_video_ext_latest ON video_ext (published_at DESC, content_id DESC);
CREATE INDEX idx_video_ext_format ON video_ext (is_short, published_at DESC, content_id DESC);
CREATE INDEX idx_video_ext_source ON video_ext (source_id, seen_at);

-- The editor's chosen video solution, like editorial_content_id; NULL lets the sync auto-link.
ALTER TABLE problem_ext ADD COLUMN video_content_id bigint REFERENCES content (id) ON DELETE SET NULL;

-- Collections mirrored from elsewhere (YouTube playlists → SERIES), upserted by their origin.
ALTER TABLE collection ADD COLUMN source text;
ALTER TABLE collection ADD COLUMN external_ref text;
CREATE UNIQUE INDEX uq_collection_external ON collection (source, external_ref) WHERE external_ref IS NOT NULL;

-- Exact-title matching of videos to problems. One normalised form, used by the index and every
-- lookup, so "Trapping Rain-Water" and "trapping rain water" meet.
-- +goose StatementBegin
CREATE FUNCTION norm_title(t text) RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT btrim(lower(regexp_replace(t, '[^[:alnum:]]+', ' ', 'g')))
$$;
-- +goose StatementEnd
CREATE INDEX idx_content_problem_title ON content (norm_title(title))
    WHERE type = 'PROBLEM' AND status = 'PUBLISHED' AND deleted_at IS NULL;

SELECT assign_id_range(current_id_shard());

-- +goose Down
DROP INDEX idx_content_problem_title;
DROP FUNCTION norm_title(text);
DROP INDEX uq_collection_external;
ALTER TABLE collection DROP COLUMN external_ref;
ALTER TABLE collection DROP COLUMN source;
ALTER TABLE problem_ext DROP COLUMN video_content_id;
DROP TABLE video_ext;
DROP TABLE video_source;
ALTER TABLE content_link DROP CONSTRAINT content_link_kind_check;
ALTER TABLE content_link ADD CONSTRAINT content_link_kind_check
    CHECK (kind IN ('PRACTICE', 'RELATED', 'PREREQUISITE', 'EDITORIAL'));
ALTER TABLE content DROP CONSTRAINT content_type_check;
ALTER TABLE content ADD CONSTRAINT content_type_check
    CHECK (type IN ('ARTICLE', 'BLOG', 'PROBLEM', 'TOPIC_PAGE', 'QUIZ'));
