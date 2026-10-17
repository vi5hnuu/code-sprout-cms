// Package testenv gives integration tests real Postgres and Redis connections. Tests that need
// them are skipped (not failed) when the variables are unset, so `go test ./...` works on a
// machine without the dev stack; `make test` sets them up and runs everything.
package testenv

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/platform/db"
)

func DB(t *testing.T) *db.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make test`")
	}
	d, err := db.Open(context.Background(), url, url, 10)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func Redis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not set; run `make test`")
	}
	// DB 15 keeps test keys away from dev data in DB 0.
	c := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect test redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// StorageEndpoint returns the S3 endpoint for storage integration tests (buckets
// sprout-public and sprout-private, created by `make storage-init`).
func StorageEndpoint(t *testing.T) string {
	t.Helper()
	ep := os.Getenv("TEST_STORAGE_ENDPOINT")
	if ep == "" {
		t.Skip("TEST_STORAGE_ENDPOINT not set; run `make test`")
	}
	return ep
}
