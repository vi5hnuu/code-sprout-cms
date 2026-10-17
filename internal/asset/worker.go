package asset

import (
	"context"
	"log/slog"
	"time"
)

// RunVerifier drains VERIFYING files, then polls. Each file is leased, so any number of
// workers can run this concurrently.
func (s *Service) RunVerifier(ctx context.Context, idle time.Duration) {
	for {
		worked, err := s.VerifyNext(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("file verification failed", "err", err)
		}
		if worked && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(idle):
		}
	}
}
