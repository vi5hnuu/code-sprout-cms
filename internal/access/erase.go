package access

import (
	"context"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/access/accessdb"
)

// EraseUser removes every editorial role the user held (account erase), inside the caller's
// transaction.
func EraseUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	return accessdb.New(tx).DeleteUserGrants(ctx, userID)
}
