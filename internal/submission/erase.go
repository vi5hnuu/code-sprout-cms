package submission

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/submission/submissiondb"
)

// EraseUser deletes a reader's submissions, solved state and activity (account erase), inside
// the caller's transaction. Problem acceptance counters keep their history: they hold no
// personal data. A submission still being judged simply finds no row when it finishes.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	q := submissiondb.New(tx)
	if err := q.DeleteUserSubmissions(ctx, userID); err != nil {
		return err
	}
	if err := q.DeleteUserProblems(ctx, userID); err != nil {
		return err
	}
	return q.DeleteUserActivity(ctx, userID)
}
