-- "Top" order for a page's discussion: top-level comments by score, then newest id. Serves
-- GET /v1/public/threads/{id}?sort=top as one index range per page (keyset on score, id).
--
-- Cost, accepted deliberately: score is now indexed, so a vote's score update can no longer
-- be a HOT update and also writes the comment's index entries. Votes are rare next to reads,
-- and the thread response is CDN-cached, so reads are unaffected. Dropping this index (and the
-- sort option) removes the cost.

-- +goose Up
CREATE INDEX idx_comment_roots_top ON comment (content_id, score DESC, id DESC) WHERE root_id IS NULL;

-- +goose Down
DROP INDEX idx_comment_roots_top;
