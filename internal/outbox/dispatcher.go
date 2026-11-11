package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sync"
	"time"

	"code-sprout-cms/internal/outbox/outboxdb"
	"code-sprout-cms/internal/platform/db"
	"code-sprout-cms/internal/platform/text"
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

// Dispatcher delivers outbox events to handlers.
//
// Each leased batch is handled by `concurrency` lanes at once. Events are assigned to a lane by
// aggregate id, so events about the same thing (publish, then unpublish, of one page) are still
// handled in order, while unrelated aggregates proceed in parallel: a bulk import or a video
// reconcile no longer holds up everything behind it at one HTTP round trip per event.
//
// Identical events in one batch (same topic, aggregate and payload: a lively comment thread
// publishes dozens of `comments.changed` for one page) are handled once; the rest are marked
// with the same outcome. Handlers re-read current state, so one delivery covers all of them.
type Dispatcher struct {
	db          *db.DB
	handlers    map[string][]Handler
	batch       int32
	idle        time.Duration // poll interval when the queue is empty
	concurrency int
	// onHandled observes every handler run's outcome (metrics); nil when nothing listens.
	onHandled func(topic string, err error, took time.Duration)
}

// OnHandled sets an observer called after each event with its outcome and handling time.
func (d *Dispatcher) OnHandled(fn func(topic string, err error, took time.Duration)) {
	d.onHandled = fn
}

// NewDispatcher returns a dispatcher that handles each batch on `concurrency` lanes (at least 1).
func NewDispatcher(d *db.DB, concurrency int) *Dispatcher {
	return &Dispatcher{db: d, handlers: map[string][]Handler{}, batch: 100, idle: time.Second, concurrency: max(1, concurrency)}
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

// group is one distinct event of a batch and every leased row it stands for.
type group struct {
	event Event
	rows  []outboxdb.LeaseDueEventsRow
}

// dispatchBatch leases a batch in a short transaction, then runs handlers outside any
// transaction (so slow downstream calls never hold locks or a connection) and records each
// outcome individually.
func (d *Dispatcher) dispatchBatch(ctx context.Context) (int, error) {
	q := outboxdb.New(d.db.Primary)
	rows, err := q.LeaseDueEvents(ctx, outboxdb.LeaseDueEventsParams{LeaseSeconds: int32(leaseFor.Seconds()), Batch: d.batch})
	if err != nil {
		return 0, fmt.Errorf("lease: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	lanes := d.assignLanes(coalesce(rows))
	errs := make([]error, len(lanes))
	var wg sync.WaitGroup
	for i, lane := range lanes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = d.runLane(ctx, q, lane)
		}()
	}
	wg.Wait()
	return len(rows), errors.Join(errs...)
}

// coalesce folds identical events into one group each, keeping the batch's order of first
// appearance (the lease orders by due time, so this is also delivery order within a lane).
func coalesce(rows []outboxdb.LeaseDueEventsRow) []*group {
	type key struct {
		topic   string
		id      int64
		payload string
	}
	byKey := map[key]*group{}
	var groups []*group
	for _, row := range rows {
		k := key{row.Topic, row.AggregateID, string(row.Payload)}
		g, ok := byKey[k]
		if !ok {
			g = &group{event: Event{ID: row.ID, Topic: row.Topic, AggregateID: row.AggregateID, Payload: row.Payload, Attempts: row.Attempts}}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, row)
	}
	return groups
}

// assignLanes spreads groups over the lanes by aggregate id, so one aggregate's events stay
// ordered on one lane. Empty lanes are dropped.
func (d *Dispatcher) assignLanes(groups []*group) [][]*group {
	lanes := make([][]*group, d.concurrency)
	for _, g := range groups {
		h := fnv.New64a()
		fmt.Fprintf(h, "%d", g.event.AggregateID)
		lane := int(h.Sum64() % uint64(d.concurrency))
		lanes[lane] = append(lanes[lane], g)
	}
	out := lanes[:0]
	for _, lane := range lanes {
		if len(lane) > 0 {
			out = append(out, lane)
		}
	}
	return out
}

// runLane handles its groups in order and records every row's outcome. A failure to record an
// outcome ends the lane: the lease will lapse and the events be redelivered, which is safe.
func (d *Dispatcher) runLane(ctx context.Context, q *outboxdb.Queries, lane []*group) error {
	for _, g := range lane {
		e := g.event
		start := time.Now()
		herr := d.handle(ctx, e)
		if d.onHandled != nil {
			d.onHandled(e.Topic, herr, time.Since(start))
		}
		if herr != nil {
			slog.Warn("outbox event failed", "id", e.ID, "topic", e.Topic, "attempt", e.Attempts+1, "coalesced", len(g.rows), "err", herr)
			msg := text.Clip(herr.Error(), 2000)
			for _, row := range g.rows {
				if err := q.MarkFailed(ctx, outboxdb.MarkFailedParams{
					CreatedAt: row.CreatedAt, ID: row.ID, LastError: &msg, AvailableAt: time.Now().Add(Backoff(row.Attempts + 1)),
				}); err != nil {
					return fmt.Errorf("mark failed: %w", err)
				}
			}
			continue
		}
		for _, row := range g.rows {
			if err := q.MarkProcessed(ctx, outboxdb.MarkProcessedParams{CreatedAt: row.CreatedAt, ID: row.ID}); err != nil {
				return fmt.Errorf("mark processed: %w", err)
			}
		}
	}
	return nil
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
