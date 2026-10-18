-- name: InsertSubmission :one
INSERT INTO submission (user_id, problem_id, language, code)
VALUES (sqlc.arg(user_id), sqlc.arg(problem_id), sqlc.arg(language), sqlc.arg(code))
RETURNING *;

-- name: GetSubmission :one
SELECT * FROM submission WHERE user_id = $1 AND id = $2;

-- Listings omit the code (up to 64 KiB each); fetch one submission to see it.
-- name: ListUserSubmissions :many
SELECT id, user_id, problem_id, language, status, verdict, passed, total, time_ms, created_at, finished_at
FROM submission
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.narg(problem_id)::bigint IS NULL OR problem_id = sqlc.narg(problem_id)::bigint)
  AND (sqlc.narg(after_created)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_created)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- Leases the oldest waiting submission, or one whose runner died (lease expired). The lease,
-- not a held lock, marks it taken, so judging never keeps a transaction open.
-- name: LeaseSubmission :one
UPDATE submission s
SET status = 'RUNNING', lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::int), attempts = s.attempts + 1
FROM (
    SELECT user_id, id FROM submission
    WHERE (status = 'QUEUED' AND (lease_until IS NULL OR lease_until < now()))
       OR (status = 'RUNNING' AND lease_until < now())
    ORDER BY created_at
    LIMIT 1
    FOR UPDATE SKIP LOCKED
) next
WHERE s.user_id = next.user_id AND s.id = next.id
RETURNING s.*;

-- name: SetSubmissionJobs :exec
UPDATE submission SET judge_jobs = sqlc.arg(jobs)::text[], test_set_id = sqlc.narg(test_set_id)
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- Hands a submission back to the queue after a transient judge failure, not before `retry_at`.
-- name: RequeueSubmission :exec
UPDATE submission SET status = 'QUEUED', lease_until = sqlc.arg(retry_at)
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- Zero rows means the submission is gone (its reader erased their account meanwhile).
-- name: FinishSubmission :execrows
UPDATE submission
SET status = sqlc.arg(status), verdict = sqlc.narg(verdict), passed = sqlc.arg(passed), total = sqlc.arg(total),
    time_ms = sqlc.narg(time_ms), failed_index = sqlc.narg(failed_index), message = sqlc.narg(message),
    lease_until = NULL, finished_at = now()
WHERE user_id = sqlc.arg(user_id) AND id = sqlc.arg(id);

-- name: UpsertUserProblem :exec
INSERT INTO user_problem (user_id, problem_id, attempts, first_solved_at, last_submitted_at)
VALUES (sqlc.arg(user_id), sqlc.arg(problem_id), 1, CASE WHEN sqlc.arg(solved)::bool THEN now() END, now())
ON CONFLICT (user_id, problem_id) DO UPDATE
SET attempts = user_problem.attempts + 1,
    first_solved_at = COALESCE(user_problem.first_solved_at, EXCLUDED.first_solved_at),
    last_submitted_at = now();

-- name: AddActivity :exec
INSERT INTO user_activity_day (user_id, day, submissions, accepted)
VALUES (sqlc.arg(user_id), current_date, 1, sqlc.arg(accepted))
ON CONFLICT (user_id, day) DO UPDATE
SET submissions = user_activity_day.submissions + 1, accepted = user_activity_day.accepted + EXCLUDED.accepted;

-- name: ProblemStates :many
SELECT problem_id, first_solved_at FROM user_problem
WHERE user_id = sqlc.arg(user_id) AND problem_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: ListSolved :many
SELECT problem_id, first_solved_at FROM user_problem
WHERE user_id = sqlc.arg(user_id) AND first_solved_at IS NOT NULL
  AND (sqlc.narg(after_solved)::timestamptz IS NULL
       OR (first_solved_at, problem_id) < (sqlc.narg(after_solved)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY first_solved_at DESC, problem_id DESC
LIMIT sqlc.arg(row_limit);

-- name: SolvedProblemIDs :many
SELECT problem_id FROM user_problem WHERE user_id = $1 AND first_solved_at IS NOT NULL;

-- name: CountUserSubmissions :one
SELECT count(*) FROM submission WHERE user_id = $1;

-- name: ActivitySince :many
SELECT day, submissions, accepted FROM user_activity_day WHERE user_id = $1 AND day >= $2 ORDER BY day;

-- name: AcceptedByLanguage :many
SELECT language, count(DISTINCT problem_id)::int AS solved
FROM submission WHERE user_id = $1 AND verdict = 'AC'
GROUP BY language ORDER BY solved DESC;

-- Account erase.

-- name: DeleteUserSubmissions :exec
DELETE FROM submission WHERE user_id = $1;

-- name: DeleteUserProblems :exec
DELETE FROM user_problem WHERE user_id = $1;

-- name: DeleteUserActivity :exec
DELETE FROM user_activity_day WHERE user_id = $1;

-- name: DeleteUserReviews :exec
DELETE FROM user_review WHERE user_id = $1;

-- Spaced review (internal/submission/review.go).

-- name: GetReviewForUpdate :one
SELECT state, step, due_at FROM user_review
WHERE user_id = sqlc.arg(user_id) AND problem_id = sqlc.arg(problem_id)
FOR UPDATE;

-- name: UpsertReview :exec
INSERT INTO user_review (user_id, problem_id, state, step, due_at, last_result_at)
VALUES (sqlc.arg(user_id), sqlc.arg(problem_id), sqlc.arg(state), sqlc.arg(step), sqlc.arg(due_at), now())
ON CONFLICT (user_id, problem_id)
DO UPDATE SET state = EXCLUDED.state, step = EXCLUDED.step, due_at = EXCLUDED.due_at, last_result_at = now();

-- name: DismissReview :execrows
UPDATE user_review SET state = 'DISMISSED'
WHERE user_id = sqlc.arg(user_id) AND problem_id = sqlc.arg(problem_id) AND state = 'ACTIVE';

-- Reviews due now, oldest first; keyset paged on (due_at, problem_id). Two queries rather than
-- one with a flag, so each is a plain range scan of idx_user_review_due.
-- name: ListDueReviews :many
SELECT problem_id, step, due_at FROM user_review
WHERE user_id = sqlc.arg(user_id) AND state = 'ACTIVE' AND due_at <= now()
  AND (sqlc.narg(after_due)::timestamptz IS NULL
       OR (due_at, problem_id) > (sqlc.narg(after_due)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY due_at, problem_id
LIMIT sqlc.arg(lim);

-- name: ListUpcomingReviews :many
SELECT problem_id, step, due_at FROM user_review
WHERE user_id = sqlc.arg(user_id) AND state = 'ACTIVE' AND due_at > now()
  AND (sqlc.narg(after_due)::timestamptz IS NULL
       OR (due_at, problem_id) > (sqlc.narg(after_due)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY due_at, problem_id
LIMIT sqlc.arg(lim);

-- name: CountDueReviews :one
SELECT count(*) FROM user_review WHERE user_id = $1 AND state = 'ACTIVE' AND due_at <= now();
