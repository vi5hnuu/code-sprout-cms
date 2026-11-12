-- Comment votes: a reader marks a comment helpful. Upvotes only, one per reader per comment,
-- so a vote is a row and comment.score (00006) is the number of rows, kept in the same
-- transaction as the insert or delete.
--
-- Partitioned like `comment`, by content_id, because every query carries the page: casting or
-- withdrawing a vote, and "which of these comments on this page did I vote for". The key leads
-- with (content_id, user_id) so that last question is one index range in one partition.

-- +goose Up
CREATE TABLE comment_vote (
    content_id bigint      NOT NULL,
    user_id    bigint      NOT NULL,
    comment_id bigint      NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, user_id, comment_id)
) PARTITION BY HASH (content_id);
SELECT create_hash_partitions('comment_vote', 32);
-- Account erase finds a reader's votes across every partition.
CREATE INDEX idx_comment_vote_user ON comment_vote (user_id);

-- +goose Down
DROP TABLE comment_vote;
