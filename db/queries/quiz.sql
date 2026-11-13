-- ── Quiz definition ─────────────────────────────────────────────────────────

-- name: InsertQuizExt :exec
INSERT INTO quiz_ext (content_id) VALUES ($1);

-- name: GetQuizExt :one
SELECT * FROM quiz_ext WHERE content_id = $1;

-- name: UpdateQuizSettings :exec
UPDATE quiz_ext SET pass_percent = sqlc.arg(pass_percent), shuffle = sqlc.arg(shuffle) WHERE content_id = sqlc.arg(content_id);

-- name: BumpQuizVersion :one
UPDATE quiz_ext SET version = version + 1 WHERE content_id = $1 RETURNING version;

-- name: ListQuestions :many
SELECT * FROM quiz_question WHERE quiz_id = $1 ORDER BY position;

-- name: CountQuestions :many
SELECT quiz_id, count(*)::int AS questions FROM quiz_question WHERE quiz_id = ANY (sqlc.arg(ids)::bigint[]) GROUP BY quiz_id;

-- name: DeleteQuestions :exec
DELETE FROM quiz_question WHERE quiz_id = $1;

-- name: InsertQuestion :exec
INSERT INTO quiz_question (quiz_id, position, kind, prompt_md, explanation_md, options)
VALUES (sqlc.arg(quiz_id), sqlc.arg(position), sqlc.arg(kind), sqlc.arg(prompt_md), sqlc.narg(explanation_md), sqlc.arg(options));

-- ── Attempts ────────────────────────────────────────────────────────────────

-- name: InsertAttempt :one
INSERT INTO quiz_attempt (user_id, quiz_id, version, correct, total, percent, passed, answers)
VALUES (sqlc.arg(user_id), sqlc.arg(quiz_id), sqlc.arg(version), sqlc.arg(correct), sqlc.arg(total), sqlc.arg(percent), sqlc.arg(passed), sqlc.arg(answers))
RETURNING id, created_at;

-- name: UpsertUserQuiz :exec
INSERT INTO user_quiz (user_id, quiz_id, attempts, best_percent, last_percent, first_passed_at, last_attempt_at)
VALUES (sqlc.arg(user_id), sqlc.arg(quiz_id), 1, sqlc.arg(percent), sqlc.arg(percent), CASE WHEN sqlc.arg(passed)::bool THEN now() END, now())
ON CONFLICT (user_id, quiz_id) DO UPDATE
SET attempts        = user_quiz.attempts + 1,
    best_percent    = GREATEST(user_quiz.best_percent, EXCLUDED.best_percent),
    last_percent    = EXCLUDED.last_percent,
    first_passed_at = COALESCE(user_quiz.first_passed_at, EXCLUDED.first_passed_at),
    last_attempt_at = now();

-- name: ListAttempts :many
SELECT id, quiz_id, version, correct, total, percent, passed, created_at FROM quiz_attempt
WHERE user_id = sqlc.arg(user_id) AND quiz_id = sqlc.arg(quiz_id)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: UserQuizStates :many
SELECT * FROM user_quiz WHERE user_id = sqlc.arg(user_id) AND quiz_id = ANY (sqlc.arg(ids)::bigint[]);

-- ── Account erase ───────────────────────────────────────────────────────────

-- name: DeleteUserAttempts :exec
DELETE FROM quiz_attempt WHERE user_id = $1;

-- name: DeleteUserQuizzes :exec
DELETE FROM user_quiz WHERE user_id = $1;
