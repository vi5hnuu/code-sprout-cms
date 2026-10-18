-- Spaced review: each problem a reader has solved comes back for review at growing intervals
-- (see internal/submission/review.go). One row per (reader, problem) once solved; the state
-- records whether it is still scheduled, mastered (passed every interval) or dismissed by the
-- reader, so a later solve never puts a dismissed problem back. Hash-partitioned by user_id like the other
-- per-reader tables: every query is "this reader's reviews".

-- +goose Up
CREATE TABLE user_review (
    user_id        bigint      NOT NULL,
    problem_id     bigint      NOT NULL,
    state          text        NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'MASTERED', 'DISMISSED')),
    step           smallint    NOT NULL DEFAULT 0 CHECK (step >= 0),
    due_at         timestamptz NOT NULL,
    last_result_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, problem_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('user_review', 16);
-- "Due now" and "coming up" pages walk this index in due order.
CREATE INDEX idx_user_review_due ON user_review (user_id, due_at, problem_id) WHERE state = 'ACTIVE';

-- +goose Down
DROP TABLE user_review;
