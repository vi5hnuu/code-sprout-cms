package engagement

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"code-sprout-cms/internal/engagement/engagementdb"
	"code-sprout-cms/internal/platform/apperr"
)

// Views and shares are far too frequent to write to Postgres one at a time. Each hit is
// de-duplicated per viewer in Redis, then counted into a Redis hash of pending deltas; the
// worker periodically renames that hash (atomically detaching it from new writes), applies the
// totals in one transaction, and deletes it. Counts are therefore a few seconds behind and, if
// the worker dies between commit and delete, a batch can be applied twice: acceptable for
// view and share counters, which is why likes/comments/bookmarks do NOT use this path.

const (
	pendingPrefix  = "eng:pending:"  // eng:pending:views → hash contentID → delta
	flushingPrefix = "eng:flushing:" // a detached batch being applied
	inFlightSet    = "eng:inflight"  // keys of detached batches not yet applied
	viewDedupe     = 30 * time.Minute
	shareDedupe    = time.Hour
)

// detachScript renames the pending hash (if any) to a batch key and records the batch as in
// flight, atomically: every increment lands in exactly one batch, and a batch is never
// detached without being tracked.
var detachScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('RENAME', KEYS[1], KEYS[2])
redis.call('SADD', KEYS[3], KEYS[2])
return 1
`)

type counter string

const (
	counterViews  counter = "views"
	counterShares counter = "shares"
)

var shareChannels = map[string]bool{"copy": true, "x": true, "linkedin": true, "facebook": true, "whatsapp": true, "email": true, "other": true}

// RecordView counts one view per viewer per item per 30 minutes. viewer is a user id or, for
// anonymous readers, a client address; it is only used as a dedupe key.
func (s *Service) RecordView(ctx context.Context, contentID int64, viewer string) error {
	return s.record(ctx, counterViews, contentID, "v:"+viewer, viewDedupe)
}

func (s *Service) RecordShare(ctx context.Context, contentID int64, viewer, channel string) error {
	if !shareChannels[channel] {
		return apperr.ValidationFailed(map[string]string{"channel": "Unknown share channel."})
	}
	return s.record(ctx, counterShares, contentID, "s:"+channel+":"+viewer, shareDedupe)
}

func (s *Service) record(ctx context.Context, c counter, contentID int64, dedupeKey string, ttl time.Duration) error {
	if contentID <= 0 {
		return apperr.NotFound("content.not_found", "No such content.")
	}
	first, err := s.rdb.SetNX(ctx, "eng:seen:"+strconv.FormatInt(contentID, 10)+":"+dedupeKey, 1, ttl).Result()
	if err != nil || !first {
		return err
	}
	return s.rdb.HIncrBy(ctx, pendingPrefix+string(c), strconv.FormatInt(contentID, 10), 1).Err()
}

// FlushCounters applies buffered view/share deltas. Batches left behind by a crashed worker
// (still under eng:flushing:*) are applied first.
func (s *Service) FlushCounters(ctx context.Context) error {
	leftovers, err := s.rdb.SMembers(ctx, inFlightSet).Result()
	if err != nil {
		return err
	}
	for _, key := range leftovers {
		if err := s.applyBatch(ctx, key); err != nil {
			return err
		}
	}
	for _, c := range []counter{counterViews, counterShares} {
		key, err := s.detach(ctx, c)
		if err != nil {
			return err
		}
		if key == "" {
			continue
		}
		if err := s.applyBatch(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

// detach moves the pending hash aside so new hits start a fresh one; "" means nothing pending.
func (s *Service) detach(ctx context.Context, c counter) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	key := flushingPrefix + string(c) + ":" + hex.EncodeToString(b[:])
	moved, err := detachScript.Run(ctx, s.rdb, []string{pendingPrefix + string(c), key, inFlightSet}).Int()
	if err != nil || moved == 0 {
		return "", err
	}
	return key, nil
}

func (s *Service) applyBatch(ctx context.Context, key string) error {
	kind, _, ok := strings.Cut(strings.TrimPrefix(key, flushingPrefix), ":")
	if !ok {
		return fmt.Errorf("malformed counter batch key %q", key)
	}
	raw, err := s.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return err
	}
	pending := make(map[int64]int64, len(raw))
	ids := make([]int64, 0, len(raw))
	for k, v := range raw {
		id, err1 := strconv.ParseInt(k, 10, 64)
		n, err2 := strconv.ParseInt(v, 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		pending[id] = n
		ids = append(ids, id)
	}
	// Hits on ids that are not live (typos, deleted pages, crafted requests) are dropped here
	// rather than checked per request, keeping the hot path a single Redis round trip.
	live, err := s.content.LiveIDs(ctx, ids)
	if err != nil {
		return err
	}
	err = s.db.InTx(ctx, func(tx pgx.Tx) error {
		q := engagementdb.New(tx)
		for _, id := range live {
			d := deltas{}
			if counter(kind) == counterViews {
				d.views = pending[id]
			} else {
				d.shares = pending[id]
			}
			if err := applyDeltas(ctx, q, id, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.SRem(ctx, inFlightSet, key)
	_, err = pipe.Exec(ctx)
	return err
}

// RunCounterFlusher flushes every interval until ctx ends, then once more so a clean shutdown
// loses nothing.
func (s *Service) RunCounterFlusher(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := s.FlushCounters(final); err != nil {
				slog.Error("final counter flush failed", "err", err)
			}
			cancel()
			return
		case <-t.C:
			if err := s.FlushCounters(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("counter flush failed", "err", err)
			}
		}
	}
}
