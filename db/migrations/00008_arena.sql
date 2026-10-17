-- Practice arena: hints, problem of the day, submissions and per-user progress, notes.
-- Per-user tables are hash-partitioned by user_id: every query is "this user's ...".

-- +goose Up
CREATE TABLE problem_hint (
    problem_id bigint NOT NULL REFERENCES problem_ext (content_id) ON DELETE CASCADE,
    position   int    NOT NULL,
    body_md    text   NOT NULL,
    PRIMARY KEY (problem_id, position)
);

CREATE TABLE problem_of_day (
    day          date        PRIMARY KEY,
    problem_id   bigint      NOT NULL REFERENCES problem_ext (content_id) ON DELETE CASCADE,
    scheduled_by bigint      REFERENCES app_user (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- One judged attempt. The worker leases QUEUED rows (lease_until), runs the hidden tests in
-- batches on ls-judge, and finalises verdict and counters. RUNNING rows whose lease expired
-- belong to a crashed worker and are leased again.
CREATE TABLE submission (
    id           bigint GENERATED ALWAYS AS IDENTITY,
    user_id      bigint      NOT NULL,
    problem_id   bigint      NOT NULL,
    language     text        NOT NULL,
    code         text        NOT NULL CHECK (octet_length(code) <= 65536),
    status       text        NOT NULL DEFAULT 'QUEUED' CHECK (status IN ('QUEUED', 'RUNNING', 'DONE', 'FAILED')),
    verdict      text        CHECK (verdict IN ('AC', 'WA', 'TLE', 'MLE', 'RE', 'CE', 'IE')),
    passed       int         NOT NULL DEFAULT 0,
    total        int         NOT NULL DEFAULT 0,
    time_ms      int,
    -- First failing case, shown to the user only when it is a sample (hidden tests stay hidden).
    failed_index int,
    message      text,                                 -- compiler output or error summary
    test_set_id  bigint,
    -- Judge jobs this submission was split into, recorded right after dispatch so a runner
    -- that restarts resumes polling them instead of dispatching again.
    judge_jobs   text[],
    lease_until  timestamptz,
    attempts     int         NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    PRIMARY KEY (user_id, id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('submission', 32);
CREATE INDEX idx_submission_user_recent ON submission (user_id, created_at DESC, id DESC);
CREATE INDEX idx_submission_user_problem ON submission (user_id, problem_id, created_at DESC, id DESC);
CREATE INDEX idx_submission_pending ON submission (created_at) WHERE status IN ('QUEUED', 'RUNNING');

CREATE TABLE user_problem (
    user_id           bigint      NOT NULL,
    problem_id        bigint      NOT NULL,
    attempts          int         NOT NULL DEFAULT 0,
    first_solved_at   timestamptz,
    last_submitted_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, problem_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('user_problem', 32);
CREATE INDEX idx_user_problem_solved ON user_problem (user_id, first_solved_at DESC, problem_id DESC)
    WHERE first_solved_at IS NOT NULL;

-- Daily activity for the heatmap and streaks (one row per user per active day).
CREATE TABLE user_activity_day (
    user_id     bigint NOT NULL,
    day         date   NOT NULL,
    submissions int    NOT NULL DEFAULT 0,
    accepted    int    NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('user_activity_day', 16);

-- Private notes a reader keeps on any item (problem approach, article takeaways).
CREATE TABLE user_note (
    user_id    bigint      NOT NULL,
    content_id bigint      NOT NULL,
    body_md    text        NOT NULL CHECK (octet_length(body_md) <= 65536),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, content_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('user_note', 16);
CREATE INDEX idx_user_note_recent ON user_note (user_id, updated_at DESC, content_id DESC);

-- +goose Down
DROP TABLE user_note;
DROP TABLE user_activity_day;
DROP TABLE user_problem;
DROP TABLE submission;
DROP TABLE problem_of_day;
DROP TABLE problem_hint;
