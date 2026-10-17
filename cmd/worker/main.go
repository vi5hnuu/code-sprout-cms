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

	"code-sprout-cms/internal/asset"
	"code-sprout-cms/internal/maintenance"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/platform/config"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/storage"
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

	var wg sync.WaitGroup
	wg.Go(func() { dispatcher.Run(ctx) })
	wg.Go(func() { maintenance.KeepPartitionsAhead(ctx, database.Primary) })
	wg.Go(func() { assets.RunVerifier(ctx, 5*time.Second) })

	slog.Info("worker started")
	wg.Wait()
	slog.Info("worker stopped")
	return nil
}
