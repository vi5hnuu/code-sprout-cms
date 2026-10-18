-- "Pick a random problem" (GET /v1/public/random-problem) jumps to a random point in the id
-- space of listed problems and takes the next one: an index range scan, never a sort of the
-- whole catalogue (ORDER BY random() reads every row).

-- +goose Up
CREATE INDEX idx_problem_listed_id ON problem_ext (content_id) WHERE listed_at IS NOT NULL;
CREATE INDEX idx_problem_listed_difficulty_id ON problem_ext (difficulty, content_id) WHERE listed_at IS NOT NULL;

-- +goose Down
DROP INDEX idx_problem_listed_difficulty_id;
DROP INDEX idx_problem_listed_id;
