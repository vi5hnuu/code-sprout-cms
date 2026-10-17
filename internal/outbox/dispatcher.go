package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/outbox/outboxdb"
	"code-sprout-cms/internal/platform/db"
)

type Event struct {
	ID          int64
	Topic       string
	AggregateID int64
	Payload     json.RawMessage
	Attempts    int32
}

// Handler reacts to one event. It must be idempotent; returning an error schedules a retry.
type Handler func(ctx context.Context, e Event) error

type Dispatcher struct {
	db       *db.DB
	handlers map[string][]Handler
	batch    int32
	idle     time.Duration // poll interval when the queue is empty
}

func NewDispatcher(d *db.DB) *Dispatcher {
	return &Dispatcher{db: d, handlers: map[string][]Handler{}, batch: 100, idle: time.Second}
}

func (d *Dispatcher) On(topic string, h Handler) { d.handlers[topic] = append(d.handlers[topic], h) }

// Run drains due events until ctx is cancelled. Full batches are followed immediately by the
// next claim so a backlog drains at full speed; an empty or partial batch waits `idle`.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		n, err := d.dispatchBatch(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("outbox batch failed", "err", err)
		}
		if n == int(d.batch) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.idle):
		}
	}
}

// dispatchBatch claims a batch, runs handlers, and records each outcome, all in one
// transaction, so a crash mid-batch releases the row locks and the events are simply
// claimed again. Batches stay small so the transaction stays short.
func (d *Dispatcher) dispatchBatch(ctx context.Context) (int, error) {
	var n int
	err := d.db.InTx(ctx, func(tx pgx.Tx) error {
		q := outboxdb.New(tx)
		rows, err := q.ClaimDueEvents(ctx, d.batch)
		if err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		n = len(rows)
		for _, row := range rows {
			e := Event{ID: row.ID, Topic: row.Topic, AggregateID: row.AggregateID, Payload: row.Payload, Attempts: row.Attempts}
			if herr := d.handle(ctx, e); herr != nil {
				slog.Warn("outbox event failed", "id", e.ID, "topic", e.Topic, "attempt", e.Attempts+1, "err", herr)
				msg := truncate(herr.Error(), 2000)
				if err := q.MarkFailed(ctx, outboxdb.MarkFailedParams{
					CreatedAt: row.CreatedAt, ID: row.ID, LastError: &msg, AvailableAt: time.Now().Add(Backoff(e.Attempts + 1)),
				}); err != nil {
					return fmt.Errorf("mark failed: %w", err)
				}
				continue
			}
			if err := q.MarkProcessed(ctx, outboxdb.MarkProcessedParams{CreatedAt: row.CreatedAt, ID: row.ID}); err != nil {
				return fmt.Errorf("mark processed: %w", err)
			}
		}
		return nil
	})
	return n, err
}

func (d *Dispatcher) handle(ctx context.Context, e Event) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("handler panic: %v", p)
		}
	}()
	for _, h := range d.handlers[e.Topic] {
		if err := h(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// Backoff is exponential from 5s, capped at 1h, so a downstream outage (search, CDN API) is
// retried patiently instead of hot-looping. Events are never dropped; a stuck one shows up in
// monitoring via its attempts count.
func Backoff(attempt int32) time.Duration {
	d := 5 * time.Second * time.Duration(math.Pow(2, float64(min(attempt-1, 10))))
	return min(d, time.Hour)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
