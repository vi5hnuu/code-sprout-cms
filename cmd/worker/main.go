// Command worker runs background processing: outbox delivery (search indexing, sitemaps,
// cache purges, ISR revalidation) and database upkeep. Several replicas can run at once;
// outbox claims use SKIP LOCKED and upkeep is idempotent.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/access"
	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/engagement"
	"code-sprout-cms/internal/judge"
	"code-sprout-cms/internal/maintenance"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/storage"
	"code-sprout-cms/internal/problem"
	"code-sprout-cms/internal/submission"
	"code-sprout-cms/internal/taxonomy"
	"code-sprout-cms/internal/user"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL, cfg.DatabaseReplicaURL, cfg.DatabaseMaxConns)
	if err != nil {
		return err
	}
	defer database.Close()

	// Handlers are registered here as their domains land (search, sitemap, revalidation).
	dispatcher := outbox.NewDispatcher(database)
	assets := asset.NewService(database, storage.FromConfig(cfg.Storage), asset.ConfigFrom(cfg.Storage))
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}
	policy := access.NewPolicy(database.Primary)
	taxonomies := taxonomy.NewService(database, policy)
	contents := content.NewService(database, policy, assets, taxonomies)
	users := user.NewService(database.Primary, database.Replica, assets)
	reader := content.NewReader(database, assets, taxonomies, users, engagement.NewStatsReader(database))
	engagements := engagement.NewService(database, rdb, reader, users, policy)
	// Registers the PROBLEM publish hooks so scheduled problems publish correctly here too.
	problems := problem.NewService(database, contents, reader, assets, policy)
	submissions := submission.NewService(database, rdb, judge.New(cfg.Judge.URL, cfg.Judge.APIKey), problems, problems, reader)

	var wg sync.WaitGroup
	wg.Go(func() { dispatcher.Run(ctx) })
	wg.Go(func() { maintenance.KeepPartitionsAhead(ctx, database.Primary) })
	wg.Go(func() { assets.RunVerifier(ctx, 5*time.Second) })
	wg.Go(func() { contents.RunScheduler(ctx, 30*time.Second) })
	wg.Go(func() { engagements.RunCounterFlusher(ctx, 30*time.Second) })
	wg.Go(func() { submissions.RunQueue(ctx, time.Second) })

	slog.Info("worker started")
	wg.Wait()
	slog.Info("worker stopped")
	return nil
}
