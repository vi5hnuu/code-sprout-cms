package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

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
	// onHandled observes every event's outcome (metrics); nil when nothing listens.
	onHandled func(topic string, err error, took time.Duration)
}

// OnHandled sets an observer called after each event with its outcome and handling time.
func (d *Dispatcher) OnHandled(fn func(topic string, err error, took time.Duration)) {
	d.onHandled = fn
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

// leaseFor bounds how long one batch may take before its events are handed to another worker.
// Handlers call external services (search, CDN, Next.js), each with its own short timeout.
const leaseFor = 5 * time.Minute

// dispatchBatch leases a batch in a short transaction, then runs handlers outside any
// transaction (so slow downstream calls never hold locks or a connection) and records each
// outcome individually.
func (d *Dispatcher) dispatchBatch(ctx context.Context) (int, error) {
	q := outboxdb.New(d.db.Primary)
	rows, err := q.LeaseDueEvents(ctx, outboxdb.LeaseDueEventsParams{LeaseSeconds: int32(leaseFor.Seconds()), Batch: d.batch})
	if err != nil {
		return 0, fmt.Errorf("lease: %w", err)
	}
	for _, row := range rows {
		e := Event{ID: row.ID, Topic: row.Topic, AggregateID: row.AggregateID, Payload: row.Payload, Attempts: row.Attempts}
		start := time.Now()
		herr := d.handle(ctx, e)
		if d.onHandled != nil {
			d.onHandled(e.Topic, herr, time.Since(start))
		}
		if herr != nil {
			slog.Warn("outbox event failed", "id", e.ID, "topic", e.Topic, "attempt", e.Attempts+1, "err", herr)
			msg := truncate(herr.Error(), 2000)
			if err := q.MarkFailed(ctx, outboxdb.MarkFailedParams{
				CreatedAt: row.CreatedAt, ID: row.ID, LastError: &msg, AvailableAt: time.Now().Add(Backoff(e.Attempts + 1)),
			}); err != nil {
				return len(rows), fmt.Errorf("mark failed: %w", err)
			}
			continue
		}
		if err := q.MarkProcessed(ctx, outboxdb.MarkProcessedParams{CreatedAt: row.CreatedAt, ID: row.ID}); err != nil {
			return len(rows), fmt.Errorf("mark processed: %w", err)
		}
	}
	return len(rows), nil
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
