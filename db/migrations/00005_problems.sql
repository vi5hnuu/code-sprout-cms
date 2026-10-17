-- Practice problems. The statement is the content body; this file holds what only a problem has.
-- Hidden test cases are never stored in rows: a test set is one NDJSON/zip object in storage
-- (thousands of cases, any size), versioned so a problem can switch sets atomically and old
-- submissions stay explainable.

-- +goose Up
CREATE TABLE problem_ext (
    content_id           bigint  PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    difficulty           text    NOT NULL CHECK (difficulty IN ('EASY', 'MEDIUM', 'HARD')),
    time_limit_ms        int     NOT NULL DEFAULT 2000 CHECK (time_limit_ms BETWEEN 100 AND 60000),
    memory_mb            int     NOT NULL DEFAULT 256 CHECK (memory_mb BETWEEN 16 AND 4096),
    checker              text    NOT NULL DEFAULT 'EXACT' CHECK (checker IN ('EXACT', 'TOKEN', 'FLOAT', 'CUSTOM')),
    checker_file_id      bigint  REFERENCES file (id),
    active_test_set_id   bigint,                     -- FK below (test_set references this table)
    editorial_content_id bigint  REFERENCES content (id) ON DELETE SET NULL,
    -- Acceptance counters, updated as each judged submission finalises.
    submission_count     bigint  NOT NULL DEFAULT 0,
    accepted_count       bigint  NOT NULL DEFAULT 0,
    -- Set while the problem is live and PUBLIC (kept in step by the publish hooks), so
    -- "newest EASY problems" is one index range scan however many problems exist.
    listed_at            timestamptz,
    CHECK (checker <> 'CUSTOM' OR checker_file_id IS NOT NULL)
);
CREATE INDEX idx_problem_listed ON problem_ext (listed_at DESC, content_id DESC) WHERE listed_at IS NOT NULL;
CREATE INDEX idx_problem_listed_by_difficulty ON problem_ext (difficulty, listed_at DESC, content_id DESC)
    WHERE listed_at IS NOT NULL;

CREATE TABLE test_set (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    problem_id  bigint      NOT NULL REFERENCES problem_ext (content_id) ON DELETE CASCADE,
    version     int         NOT NULL,
    file_id     bigint      NOT NULL REFERENCES file (id),
    case_count  int         NOT NULL CHECK (case_count > 0),
    total_bytes bigint      NOT NULL,
    sha256      bytea       NOT NULL,                -- judge caches test sets by checksum
    created_by  bigint      NOT NULL REFERENCES app_user (id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (problem_id, version)
);

ALTER TABLE problem_ext
    ADD CONSTRAINT fk_problem_active_test_set FOREIGN KEY (active_test_set_id) REFERENCES test_set (id);

-- The few examples shown in the statement; inline so the page renders without storage reads.
CREATE TABLE sample_case (
    problem_id  bigint NOT NULL REFERENCES problem_ext (content_id) ON DELETE CASCADE,
    position    int    NOT NULL,
    input       text   NOT NULL,
    output      text   NOT NULL,
    explanation text,
    PRIMARY KEY (problem_id, position)
);

-- Per-language starter code shown in the editor and the hidden driver that wraps it for judging.
-- A driver contains the placeholder {{USER_CODE}}, replaced by the submission before judging
-- (function-style problems); without a driver the submission runs as-is (stdin/stdout).
CREATE TABLE code_template (
    problem_id   bigint      NOT NULL REFERENCES problem_ext (content_id) ON DELETE CASCADE,
    language     text        NOT NULL,
    starter_code text        NOT NULL,
    driver_code  text,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (problem_id, language)
);

-- +goose Down
DROP TABLE code_template;
DROP TABLE sample_case;
ALTER TABLE problem_ext DROP CONSTRAINT fk_problem_active_test_set;
DROP TABLE test_set;
DROP TABLE problem_ext;
