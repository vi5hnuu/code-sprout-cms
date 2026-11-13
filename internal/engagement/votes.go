package engagement

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/auth"
	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/platform/apperr"
	"code-sprout-cms/internal/platform/db"
)

// Comment votes mark a comment helpful. They are upvotes only: a downvote mostly expresses
// disagreement, invites pile-ons in a learning community, and moderation (reports) already
// handles what should not be seen. A vote is a row in comment_vote and comment.score counts
// them, kept in the same transaction.
//
// Scores reach readers through the public thread response, which the CDN keeps for 30
// seconds; a vote does not purge it (that would be one CDN call per click). The voter sees
// their own vote at once from VotedComments and the client's optimistic update.

// VoteComment records the reader's vote. Voting again is a no-op, so retries are safe.
func (s *Service) VoteComment(ctx context.Context, p auth.Principal, contentID, commentID int64) error {
	if p.IsGuest {
		// Same bar as commenting: free throwaway accounts cannot stuff votes.
		return apperr.Forbidden("comment.vote_guest", "Create a free account to vote.")
	}
	if err := s.requireLive(ctx, contentID); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		c, err := q.GetComment(ctx, engagementdb.GetCommentParams{ContentID: contentID, ID: commentID})
		if db.IsNoRows(err) || (err == nil && CommentStatus(c.Status) != CommentVisible) {
			return apperr.NotFound("comment.not_found", "No such comment.")
		}
		if err != nil {
			return err
		}
		if c.AuthorID == p.UserID {
			return apperr.Forbidden("comment.own_vote", "You can't vote for your own comment.")
		}
		n, err := q.InsertCommentVote(ctx, engagementdb.InsertCommentVoteParams{ContentID: contentID, UserID: p.UserID, CommentID: commentID})
		if err != nil || n == 0 {
			return err
		}
		return q.BumpCommentScore(ctx, engagementdb.BumpCommentScoreParams{Delta: 1, ContentID: contentID, ID: commentID})
	})
}

// UnvoteComment withdraws the reader's vote; withdrawing one that does not exist is a no-op.
// It works on hidden or deleted comments too, so a reader can always take a vote back.
func (s *Service) UnvoteComment(ctx context.Context, p auth.Principal, contentID, commentID int64) error {
	return s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		n, err := q.DeleteCommentVote(ctx, engagementdb.DeleteCommentVoteParams{ContentID: contentID, UserID: p.UserID, CommentID: commentID})
		if err != nil || n == 0 {
			return err
		}
		return q.BumpCommentScore(ctx, engagementdb.BumpCommentScoreParams{Delta: -1, ContentID: contentID, ID: commentID})
	})
}

// VotedComments returns which of the given comments on one page the reader has voted for. The
// thread itself is public and cached for everyone, so this per-reader overlay is a separate,
// uncached call; it reads the primary so a vote just cast is always reflected.
func (s *Service) VotedComments(ctx context.Context, p auth.Principal, contentID int64, ids []int64) ([]int64, error) {
	if len(ids) > maxStateIDs {
		return nil, apperr.ValidationFailed(map[string]string{"ids": fmt.Sprintf("At most %d ids.", maxStateIDs)})
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	return engagementdb.New(s.db.Primary).VotedCommentIDs(ctx, engagementdb.VotedCommentIDsParams{ContentID: contentID, UserID: p.UserID, Ids: ids})
}
