package maintenance

import (
	"context"
	"log/slog"
	"time"
)

// Every runs job now and then at each interval until ctx ends, logging failures. Jobs must be
// idempotent: several worker replicas may run the same schedule.
func Every(ctx context.Context, name string, interval time.Duration, job func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := job(ctx); err != nil && ctx.Err() == nil {
			slog.Error("scheduled job failed", "job", name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
