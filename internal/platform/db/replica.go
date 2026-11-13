package db

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrReplicaLagging is returned when the replica has not caught up within the allowed time.
var ErrReplicaLagging = errors.New("replica has not replayed the primary's latest commits")

// WaitReplicaCaughtUp blocks until the replica has replayed everything the primary had committed
// when the call began, or returns ErrReplicaLagging after timeout.
//
// Why: the web tier re-renders a page by fetching the API, whose public reads come from the
// replica. A revalidation sent the instant a publish commits can therefore re-read the *old*
// row, and because content pages are cached until the next explicit revalidation, that stale
// render would stick. Callers that trigger a re-read after a write go through this first.
//
// On a single server (Replica == Primary) or when the "replica" URL turns out to be the primary
// (not in recovery, e.g. a pooler alias) this returns at once.
func (d *DB) WaitReplicaCaughtUp(ctx context.Context, timeout time.Duration) error {
	if d.Replica == d.Primary {
		return nil
	}
	var target string
	if err := d.Primary.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&target); err != nil {
		return fmt.Errorf("primary lsn: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		var caughtUp bool
		err := d.Replica.QueryRow(ctx,
			"SELECT NOT pg_is_in_recovery() OR COALESCE(pg_last_wal_replay_lsn() >= $1::pg_lsn, false)", target,
		).Scan(&caughtUp)
		if err != nil {
			return fmt.Errorf("replica lsn: %w", err)
		}
		if caughtUp {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrReplicaLagging
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
