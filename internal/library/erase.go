package library

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/library/librarydb"
)

// EraseUser deletes a reader's private notes (account erase), inside the caller's transaction.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	return librarydb.New(tx).DeleteUserNotes(ctx, userID)
}
