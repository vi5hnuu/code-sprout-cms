-- Quizzes: a QUIZ content item (its intro is the item's body, edited and reviewed like any
-- article) plus its questions, and each signed-in reader's attempts.
--
-- Questions keep their options inline (jsonb): options are written, read and replaced
-- together and never queried alone, so rendering or grading a quiz is one indexed read of its
-- question rows. Answer keys live in the same jsonb; the API strips them before anything
-- reaches a reader, and grading runs on the server.
--
-- Attempts are immutable history, partitioned by reader like every per-user table. user_quiz
-- is the per-reader summary (best score, passed) so listings never aggregate attempts.
-- Quiz-wide totals are deliberately not kept on quiz_ext: a counter updated by every attempt
-- would make one popular quiz's row a lock hot spot.

-- +goose Up
CREATE TABLE quiz_ext (
    content_id   bigint   PRIMARY KEY REFERENCES content (id) ON DELETE CASCADE,
    pass_percent smallint NOT NULL DEFAULT 70 CHECK (pass_percent BETWEEN 1 AND 100),
    shuffle      boolean  NOT NULL DEFAULT false,
    -- Bumped whenever the questions are replaced; an attempt records the version it answered.
    version      int      NOT NULL DEFAULT 1
);

CREATE TABLE quiz_question (
    quiz_id        bigint   NOT NULL REFERENCES quiz_ext (content_id) ON DELETE CASCADE,
    id             bigint   GENERATED ALWAYS AS IDENTITY,
    position       smallint NOT NULL,
    kind           text     NOT NULL CHECK (kind IN ('SINGLE', 'MULTI')),
    prompt_md      text     NOT NULL CHECK (length(prompt_md) BETWEEN 1 AND 4000),
    explanation_md text     CHECK (explanation_md IS NULL OR length(explanation_md) <= 4000),
    -- [{"id": 1, "body": "...", "correct": true}, ...]
    options        jsonb    NOT NULL CHECK (jsonb_typeof(options) = 'array' AND jsonb_array_length(options) BETWEEN 2 AND 10),
    PRIMARY KEY (quiz_id, id),
    UNIQUE (quiz_id, position)
);

CREATE TABLE quiz_attempt (
    user_id    bigint      NOT NULL,
    id         bigint      GENERATED ALWAYS AS IDENTITY,
    quiz_id    bigint      NOT NULL,
    version    int         NOT NULL,
    correct    smallint    NOT NULL,
    total      smallint    NOT NULL,
    percent    smallint    NOT NULL,
    passed     boolean     NOT NULL,
    -- [{"questionId": 1, "optionIds": [2]}, ...]: what the reader chose, for their history.
    answers    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('quiz_attempt', 32);
CREATE INDEX idx_quiz_attempt_user_quiz ON quiz_attempt (user_id, quiz_id, created_at DESC, id DESC);

CREATE TABLE user_quiz (
    user_id         bigint      NOT NULL,
    quiz_id         bigint      NOT NULL,
    attempts        int         NOT NULL DEFAULT 0,
    best_percent    smallint    NOT NULL,
    last_percent    smallint    NOT NULL,
    first_passed_at timestamptz,
    last_attempt_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, quiz_id)
) PARTITION BY HASH (user_id);
SELECT create_hash_partitions('user_quiz', 32);

-- New identity columns (questions, attempts) join this database's shard id range (00011).
SELECT assign_id_range(current_id_shard());

-- +goose Down
DROP TABLE user_quiz;
DROP TABLE quiz_attempt;
DROP TABLE quiz_question;
DROP TABLE quiz_ext;
