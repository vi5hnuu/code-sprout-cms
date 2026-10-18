-- name: InsertProblemExt :exec
INSERT INTO problem_ext (content_id, difficulty, time_limit_ms, memory_mb)
VALUES (sqlc.arg(content_id), sqlc.arg(difficulty), sqlc.arg(time_limit_ms), sqlc.arg(memory_mb));

-- name: GetProblemExt :one
SELECT * FROM problem_ext WHERE content_id = $1;

-- name: ListProblemExt :many
SELECT * FROM problem_ext WHERE content_id = ANY (sqlc.arg(ids)::bigint[]);

-- name: UpdateProblemSettings :exec
UPDATE problem_ext
SET difficulty = sqlc.arg(difficulty), time_limit_ms = sqlc.arg(time_limit_ms), memory_mb = sqlc.arg(memory_mb),
    checker = sqlc.arg(checker), checker_file_id = sqlc.narg(checker_file_id),
    editorial_content_id = sqlc.narg(editorial_content_id)
WHERE content_id = sqlc.arg(content_id);

-- name: SetListedAt :exec
UPDATE problem_ext SET listed_at = sqlc.narg(listed_at) WHERE content_id = sqlc.arg(content_id);

-- name: ListListedProblems :many
SELECT content_id, listed_at FROM problem_ext
WHERE listed_at IS NOT NULL
  AND (sqlc.narg(difficulty)::text IS NULL OR difficulty = sqlc.narg(difficulty)::text)
  AND (sqlc.narg(after_listed)::timestamptz IS NULL
       OR (listed_at, content_id) < (sqlc.narg(after_listed)::timestamptz, sqlc.narg(after_id)::bigint))
ORDER BY listed_at DESC, content_id DESC
LIMIT sqlc.arg(row_limit);

-- name: RecordProblemSubmission :exec
UPDATE problem_ext
SET submission_count = submission_count + 1, accepted_count = accepted_count + sqlc.arg(accepted)
WHERE content_id = sqlc.arg(content_id);

-- ── Samples, templates, hints (replaced wholesale by the studio) ────────────

-- name: DeleteSamples :exec
DELETE FROM sample_case WHERE problem_id = $1;

-- name: InsertSample :exec
INSERT INTO sample_case (problem_id, position, input, output, explanation)
VALUES (sqlc.arg(problem_id), sqlc.arg(position), sqlc.arg(input), sqlc.arg(output), sqlc.narg(explanation));

-- name: ListSamples :many
SELECT * FROM sample_case WHERE problem_id = $1 ORDER BY position;

-- name: UpsertTemplate :exec
INSERT INTO code_template (problem_id, language, starter_code, driver_code, updated_at)
VALUES (sqlc.arg(problem_id), sqlc.arg(language), sqlc.arg(starter_code), sqlc.narg(driver_code), now())
ON CONFLICT (problem_id, language) DO UPDATE
SET starter_code = EXCLUDED.starter_code, driver_code = EXCLUDED.driver_code, updated_at = now();

-- name: DeleteTemplate :execrows
DELETE FROM code_template WHERE problem_id = $1 AND language = $2;

-- name: ListTemplates :many
SELECT * FROM code_template WHERE problem_id = $1 ORDER BY language;

-- name: GetTemplate :one
SELECT * FROM code_template WHERE problem_id = $1 AND language = $2;

-- name: DeleteHints :exec
DELETE FROM problem_hint WHERE problem_id = $1;

-- name: InsertHints :exec
INSERT INTO problem_hint (problem_id, position, body_md)
SELECT sqlc.arg(problem_id), t.pos::int, t.body
FROM unnest(sqlc.arg(bodies)::text[]) WITH ORDINALITY AS t(body, pos);

-- name: ListHints :many
SELECT * FROM problem_hint WHERE problem_id = $1 ORDER BY position;

-- ── Test sets ───────────────────────────────────────────────────────────────

-- name: NextTestSetVersion :one
SELECT (COALESCE(max(version), 0) + 1)::int FROM test_set WHERE problem_id = $1;

-- name: InsertTestSet :one
INSERT INTO test_set (problem_id, version, file_id, case_count, total_bytes, sha256, created_by)
VALUES (sqlc.arg(problem_id), sqlc.arg(version), sqlc.arg(file_id), sqlc.arg(case_count), sqlc.arg(total_bytes),
        sqlc.arg(sha256), sqlc.arg(created_by))
RETURNING *;

-- name: SetActiveTestSet :exec
UPDATE problem_ext SET active_test_set_id = sqlc.arg(test_set_id) WHERE content_id = sqlc.arg(problem_id);

-- name: ListTestSets :many
SELECT * FROM test_set WHERE problem_id = $1 ORDER BY version DESC LIMIT 50;

-- name: GetTestSet :one
SELECT * FROM test_set WHERE id = $1;

-- name: GetTestSetByVersion :one
SELECT * FROM test_set WHERE problem_id = $1 AND version = $2;

-- ── Problem of the day ──────────────────────────────────────────────────────

-- name: UpsertProblemOfDay :exec
INSERT INTO problem_of_day (day, problem_id, scheduled_by) VALUES ($1, $2, $3)
ON CONFLICT (day) DO UPDATE SET problem_id = EXCLUDED.problem_id, scheduled_by = EXCLUDED.scheduled_by, created_at = now();

-- name: GetProblemOfDay :one
SELECT * FROM problem_of_day WHERE day = $1;

-- ── Random pick (problem/random.go): bounds, then the first listed id at or after a pivot ──

-- name: ListedIDBounds :one
SELECT coalesce(min(content_id), 0)::bigint AS lo, coalesce(max(content_id), 0)::bigint AS hi FROM problem_ext WHERE listed_at IS NOT NULL;

-- name: ListedIDBoundsByDifficulty :one
SELECT coalesce(min(content_id), 0)::bigint AS lo, coalesce(max(content_id), 0)::bigint AS hi FROM problem_ext
WHERE listed_at IS NOT NULL AND difficulty = $1;

-- name: ListedProblemFrom :one
SELECT content_id FROM problem_ext
WHERE listed_at IS NOT NULL AND content_id >= $1
ORDER BY content_id LIMIT 1;

-- name: ListedProblemFromByDifficulty :one
SELECT content_id FROM problem_ext
WHERE listed_at IS NOT NULL AND difficulty = sqlc.arg(difficulty) AND content_id >= sqlc.arg(pivot)
ORDER BY content_id LIMIT 1;

-- Tagged picks walk the tag's own index (content_tag primary key) in id order and keep the
-- first listed problem; difficulty, when given, is a filter on that walk.
-- name: TaggedIDBounds :one
SELECT coalesce(min(content_id), 0)::bigint AS lo, coalesce(max(content_id), 0)::bigint AS hi FROM content_tag
WHERE tag_id = $1 AND published_at IS NOT NULL;

-- name: TaggedProblemFrom :one
SELECT ct.content_id FROM content_tag ct
JOIN problem_ext pe ON pe.content_id = ct.content_id
WHERE ct.tag_id = sqlc.arg(tag_id) AND ct.published_at IS NOT NULL AND ct.content_id >= sqlc.arg(pivot)
  AND pe.listed_at IS NOT NULL
  AND (sqlc.narg(difficulty)::text IS NULL OR pe.difficulty = sqlc.narg(difficulty)::text)
ORDER BY ct.content_id LIMIT 1;
