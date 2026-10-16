// Package db owns the Postgres connection pools.
//
// Two pools by design: Primary takes every write and any read that must see the caller's own
// write (studio, /v1/me); Replica serves anonymous public reads, which are most of the traffic
// that gets past the CDN. In dev both URLs point at the same server.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Primary *pgxpool.Pool
	Replica *pgxpool.Pool
}

func Open(ctx context.Context, primaryURL, replicaURL string, maxConns int32) (*DB, error) {
	primary, err := newPool(ctx, primaryURL, maxConns)
	if err != nil {
		return nil, fmt.Errorf("primary: %w", err)
	}
	if replicaURL == primaryURL {
		return &DB{Primary: primary, Replica: primary}, nil
	}
	replica, err := newPool(ctx, replicaURL, maxConns)
	if err != nil {
		primary.Close()
		return nil, fmt.Errorf("replica: %w", err)
	}
	return &DB{Primary: primary, Replica: replica}, nil
}

func newPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	// PgBouncer in transaction mode cannot keep server-side prepared statements across
	// transactions; the describe-exec protocol keeps type safety without them.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func (d *DB) Close() {
	if d.Replica != d.Primary {
		d.Replica.Close()
	}
	d.Primary.Close()
}

func (d *DB) Ping(ctx context.Context) error {
	if err := d.Primary.Ping(ctx); err != nil {
		return fmt.Errorf("primary: %w", err)
	}
	if err := d.Replica.Ping(ctx); err != nil {
		return fmt.Errorf("replica: %w", err)
	}
	return nil
}

// InTx runs fn inside a transaction on the primary, committing on success and rolling back on
// error or panic. Domain code uses this so a change and its outbox event commit together.
func (d *DB) InTx(ctx context.Context, fn func(pgx.Tx) error) (err error) {
	tx, err := d.Primary.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Postgres error codes the domain layer translates into API errors.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
	codeCheckViolation      = "23514"
)

// ConstraintViolation reports which constraint a write broke, if any, so callers can map a
// specific constraint (e.g. uq_content_slug) to a specific user-facing error.
func ConstraintViolation(err error) (constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", false
	}
	switch pgErr.Code {
	case codeUniqueViolation, codeForeignKeyViolation, codeCheckViolation:
		return pgErr.ConstraintName, true
	}
	return "", false
}

func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
