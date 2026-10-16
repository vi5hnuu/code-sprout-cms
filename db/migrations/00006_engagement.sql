-- Reader engagement: likes, bookmarks, comments and counters. These are the tables that reach
-- billions of rows, so each is partitioned by the key it is always queried with:
--   reaction, comment → content_id ("this item's likes/comments")
--   bookmark          → user_id    ("my bookmarks")
-- Partitions are local indexes only, so every hot query must carry its partition key.
--
-- Counters are deliberately NOT computed with COUNT(*): content_stats holds them, updated in
-- the same transaction as the reaction/bookmark/comment row (likes etc.) or flushed in
-- batches from Redis (views). A nightly reconciliation job repairs any drift.

-- +goose Up
CREATE TABLE reaction (
    content_id bigint      NOT NULL,
    user_id    bigint      NOT NULL,
    kind       text        NOT NULL DEFAULT 'LIKE' CHECK (kind IN ('LIKE')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, user_id)               -- one reaction per user per item; toggle is idempotent
) PARTITION BY HASH (content_id);
SELECT create_hash_partitions('reaction', 32);
CREATE INDEX idx_reaction_user ON reaction (user_id, created_at DESC);  -- "liked by me" page

CREATE TABLE bookmark_list (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, name)
);

CREATE TABLE bookmark (
    user_id    bigint      NOT NULL,
    content_id bigint      NOT NULL,
    list_id    bigint,                              -- NULL = default list
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, content_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('bookmark', 32);
CREATE INDEX idx_bookmark_user_recent ON bookmark (user_id, created_at DESC, content_id DESC);
CREATE INDEX idx_bookmark_user_list ON bookmark (user_id, list_id, created_at DESC) WHERE list_id IS NOT NULL;

-- Threaded comments shown two levels deep (root + replies, LeetCode-style); deeper replies
-- attach to their root with parent_id recording who was answered. Comment ids are global, but
-- every lookup also carries content_id so it hits one partition.
CREATE TABLE comment (
    id          bigint GENERATED ALWAYS AS IDENTITY,
    content_id  bigint      NOT NULL,
    root_id     bigint,                             -- NULL for top-level comments
    parent_id   bigint,
    author_id   bigint      NOT NULL,
    body_md     text        NOT NULL CHECK (length(body_md) BETWEEN 1 AND 20000),
    status      text        NOT NULL DEFAULT 'VISIBLE' CHECK (status IN ('VISIBLE', 'PENDING', 'HIDDEN', 'DELETED')),
    score       int         NOT NULL DEFAULT 0,
    reply_count int         NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    edited_at   timestamptz,
    PRIMARY KEY (content_id, id),
    CHECK ((root_id IS NULL) = (parent_id IS NULL))
) PARTITION BY HASH (content_id);
SELECT create_hash_partitions('comment', 32);
CREATE INDEX idx_comment_roots ON comment (content_id, created_at DESC, id DESC) WHERE root_id IS NULL;
CREATE INDEX idx_comment_replies ON comment (content_id, root_id, created_at, id) WHERE root_id IS NOT NULL;
CREATE INDEX idx_comment_author ON comment (author_id, created_at DESC);

CREATE TABLE comment_report (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    content_id  bigint      NOT NULL,
    comment_id  bigint      NOT NULL,
    reporter_id bigint      NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    reason      text        NOT NULL CHECK (reason IN ('SPAM', 'ABUSE', 'OFF_TOPIC', 'OTHER')),
    note        text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    resolved_by bigint      REFERENCES app_user (id),
    UNIQUE (comment_id, reporter_id)
);
CREATE INDEX idx_comment_report_open ON comment_report (created_at) WHERE resolved_at IS NULL;

-- Kept out of `content` so constant counter churn never bloats or locks the content row.
-- fillfactor leaves room on each page for HOT updates (no index writes per increment).
CREATE TABLE content_stats (
    content_id bigint      PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    views      bigint      NOT NULL DEFAULT 0,
    likes      bigint      NOT NULL DEFAULT 0,
    comments   bigint      NOT NULL DEFAULT 0,
    bookmarks  bigint      NOT NULL DEFAULT 0,
    shares     bigint      NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
) WITH (fillfactor = 80);

-- Daily rollups for trending and author analytics. Monthly partitions are cheap to drop or
-- archive once they age out.
CREATE TABLE content_daily_stats (
    content_id bigint NOT NULL,
    day        date   NOT NULL,
    views      bigint NOT NULL DEFAULT 0,
    likes      int    NOT NULL DEFAULT 0,
    comments   int    NOT NULL DEFAULT 0,
    bookmarks  int    NOT NULL DEFAULT 0,
    shares     int    NOT NULL DEFAULT 0,
    PRIMARY KEY (content_id, day)
) PARTITION BY RANGE (day);
SELECT create_monthly_partitions('content_daily_stats', current_date, 4);
CREATE INDEX idx_content_daily_stats_day ON content_daily_stats (day, views DESC);

-- +goose Down
DROP TABLE content_daily_stats;
DROP TABLE content_stats;
DROP TABLE comment_report;
DROP TABLE comment;
DROP TABLE bookmark;
DROP TABLE bookmark_list;
DROP TABLE reaction;
