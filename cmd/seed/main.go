// Command seed fills a development database with sample content: sections, tags, authors,
// articles, blog posts, judged problems, study plans and the problem of the day.
//
// It goes through the same services as the API, so every item is validated, rendered,
// published and announced on the outbox exactly as authored content would be. Run the worker
// alongside it so search and sitemaps pick the items up.
//
// Re-running is safe: whatever already exists (by path, slug or handle) is reused, and an item
// left half-done by an interrupted run is finished rather than duplicated.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/collection"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/storage"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

func main() {
	allowRemote := flag.Bool("allow-remote", false, "seed a database that is not on this machine")
	check := flag.Bool("check", false, "only validate the sample bodies (no database needed)")
	flag.Parse()
	if *check {
		if err := checkBodies(); err != nil {
			slog.Error("check failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*allowRemote); err != nil {
		slog.Error("seed failed", "err", err)
		os.Exit(1)
	}
}

func run(allowRemote bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !allowRemote {
		if err := requireLocal(cfg.DatabaseURL); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL, cfg.DatabaseReplicaURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer database.Close()

	policy := access.NewPolicy(database.Primary)
	taxonomies := taxonomy.NewService(database, policy)
	assets := asset.NewService(database, storage.FromConfig(cfg.Storage), asset.ConfigFrom(cfg.Storage))
	users := user.NewService(database.Primary, database.Replica, assets)
	contents := content.NewService(database, policy, assets, taxonomies)
	reader := content.NewReader(database, assets, taxonomies, users, engagement.NewStatsReader(database))
	problems := problem.NewService(database, contents, reader, assets, policy)
	collections := collection.NewService(database, reader, policy)

	s := &seeder{
		users: users, taxonomy: taxonomies, assets: assets, contents: contents,
		problems: problems, collections: collections, db: database,
	}
	return s.run(ctx)
}

// requireLocal refuses to seed anything but a database on this machine: sample content must
// never land in a shared or production database by accident.
func requireLocal(databaseURL string) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return errors.New("DATABASE_URL is not a local database; pass -allow-remote if you really mean to seed it")
}
