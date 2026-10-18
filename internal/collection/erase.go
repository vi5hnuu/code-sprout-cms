package collection

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/collection/collectiondb"
)

// EraseUser deletes a reader's own lists (account erase). Editorial collections they own stay:
// they are the site's. Runs inside the caller's transaction.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	return collectiondb.New(tx).DeleteUserLists(ctx, userID)
}
