package asset

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/asset/assetdb"
)

// EraseUser lets go of a reader's avatars (account erase), inside the caller's transaction:
// they stop being served at once and garbage collection deletes the objects, which would
// otherwise stay reachable at their public URLs. Files they uploaded into published pages stay
// with those pages.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	return assetdb.New(tx).MarkUserFilesDeleting(ctx, &userID)
}
