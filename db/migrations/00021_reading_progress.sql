-- Where each signed-in reader left off on each page, for "Continue reading" and resume.
--
-- One row per (reader, page), overwritten as they read: the latest position is what resume
-- needs (devices differ, so the position is the section heading's anchor plus a percentage,
-- not pixels). finished_at is set the first time a reader passes 90% and never cleared, so a
-- page re-opened at the top still counts as read. This is deliberately separate from views
-- (anonymous counters, buffered in Redis) and from activity days (streak statistics).
--
-- Partitioned by user like bookmarks and notes: every query is "this reader's rows". Two
-- partial indexes serve the two lists (in progress, finished) newest first, so neither list
-- has to skip the other's rows.

-- +goose Up
CREATE TABLE reading_progress (
    user_id     bigint      NOT NULL,
    content_id  bigint      NOT NULL,
    heading_id  text        CHECK (heading_id IS NULL OR length(heading_id) BETWEEN 1 AND 200),
    percent     smallint    NOT NULL CHECK (percent BETWEEN 0 AND 100),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    PRIMARY KEY (user_id, content_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('reading_progress', 32);
CREATE INDEX idx_reading_in_progress ON reading_progress (user_id, updated_at DESC, content_id DESC) WHERE finished_at IS NULL;
CREATE INDEX idx_reading_finished ON reading_progress (user_id, finished_at DESC, content_id DESC) WHERE finished_at IS NOT NULL;

-- +goose Down
DROP TABLE reading_progress;
