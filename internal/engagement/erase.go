package engagement

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/outbox"
)

// EraseUser removes a reader's likes, saves and bookmark lists, and empties every comment they
// wrote (account erase). Counters drop by what was removed, and each page whose discussion
// changed gets a comments event so caches refresh. Runs inside the caller's transaction.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	q := engagementdb.New(tx)
	perPage := map[int64]*deltas{}
	add := func(contentID int64) *deltas {
		d, ok := perPage[contentID]
		if !ok {
			d = &deltas{}
			perPage[contentID] = d
		}
		return d
	}

	liked, err := q.DeleteUserReactions(ctx, userID)
	if err != nil {
		return err
	}
	for _, id := range liked {
		add(id).likes--
	}
	saved, err := q.DeleteUserBookmarks(ctx, userID)
	if err != nil {
		return err
	}
	for _, id := range saved {
		add(id).bookmarks--
	}
	if err := q.DeleteUserBookmarkLists(ctx, userID); err != nil {
		return err
	}

	comments, err := q.EraseUserComments(ctx, userID)
	if err != nil {
		return err
	}
	discussions := map[int64]bool{}
	replies := map[[2]int64]int32{} // (content, root) → visible replies removed
	for _, c := range comments {
		discussions[c.ContentID] = true
		if CommentStatus(c.OldStatus) != CommentVisible {
			continue
		}
		add(c.ContentID).comments--
		if c.RootID != nil {
			replies[[2]int64{c.ContentID, *c.RootID}]--
		}
	}

	for contentID, d := range perPage {
		if err := applyDeltas(ctx, q, contentID, *d); err != nil {
			return err
		}
	}
	for key, delta := range replies {
		if err := q.BumpReplyCount(ctx, engagementdb.BumpReplyCountParams{ContentID: key[0], ID: key[1], Delta: delta}); err != nil {
			return err
		}
	}
	for contentID := range discussions {
		if err := outbox.Publish(ctx, tx, outbox.TopicCommentsChanged, contentID, outbox.CommentsEvent{ContentID: contentID}); err != nil {
			return err
		}
	}
	return nil
}
