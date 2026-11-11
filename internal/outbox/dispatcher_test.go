package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"code-sprout-cms/internal/platform/testenv"
)

func TestBackoff(t *testing.T) {
	cases := map[int32]time.Duration{1: 5 * time.Second, 2: 10 * time.Second, 3: 20 * time.Second, 50: time.Hour}
	for attempt, want := range cases {
		if got := Backoff(attempt); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestDispatchDeliversCommittedEventsOnly(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	// Unique topic so events left by other tests never reach these handlers.
	topic := fmt.Sprintf("test.%d", time.Now().UnixNano())

	// One event commits, one rolls back with its transaction.
	if err := d.InTx(ctx, func(tx pgx.Tx) error {
		return Publish(ctx, tx, topic, 1, ContentEvent{Type: "ARTICLE", Locale: "en", Slug: "kept"})
	}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	_ = d.InTx(ctx, func(tx pgx.Tx) error {
		if err := Publish(ctx, tx, topic, 2, ContentEvent{Slug: "discarded"}); err != nil {
			return err
		}
		return rollback
	})

	var delivered []int64
	disp := NewDispatcher(d, 4)
	disp.On(topic, func(_ context.Context, e Event) error {
		var p ContentEvent
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return err
		}
		if p.Slug != "kept" {
			t.Errorf("unexpected payload %+v", p)
		}
		delivered = append(delivered, e.AggregateID)
		return nil
	})
	drain(t, disp)

	if len(delivered) != 1 || delivered[0] != 1 {
		t.Fatalf("delivered = %v, want [1]", delivered)
	}
	assertPending(t, d, topic, 0)
}

func TestDispatchRetriesFailuresWithBackoff(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	topic := fmt.Sprintf("test.%d", time.Now().UnixNano())

	if err := d.InTx(ctx, func(tx pgx.Tx) error { return Publish(ctx, tx, topic, 7, struct{}{}) }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	disp := NewDispatcher(d, 4)
	disp.On(topic, func(context.Context, Event) error { calls++; return errors.New("search is down") })
	drain(t, disp)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (retry must wait for backoff)", calls)
	}
	var attempts int
	var lastErr string
	var availableAt time.Time
	err := d.Primary.QueryRow(ctx,
		"SELECT attempts, last_error, available_at FROM outbox WHERE topic = $1", topic).Scan(&attempts, &lastErr, &availableAt)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastErr != "search is down" || time.Until(availableAt) < 3*time.Second {
		t.Fatalf("attempts=%d lastErr=%q availableIn=%v", attempts, lastErr, time.Until(availableAt))
	}
	assertPending(t, d, topic, 1)
}

// drain runs batches until no due events remain. Events from other topics may be claimed too;
// with no handlers registered they are marked processed, which is harmless in the test database.
func drain(t *testing.T, disp *Dispatcher) {
	t.Helper()
	for {
		n, err := disp.dispatchBatch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if n < int(disp.batch) {
			return
		}
	}
}

func assertPending(t *testing.T, d interface {
	InTx(context.Context, func(pgx.Tx) error) error
}, topic string, want int) {
	t.Helper()
	var got int
	err := d.InTx(context.Background(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			"SELECT count(*) FROM outbox WHERE topic = $1 AND processed_at IS NULL", topic).Scan(&got)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pending events = %d, want %d", got, want)
	}
}

// Identical events in one batch are handled once, and events about one aggregate are handled
// in order even with several lanes; all rows end up processed.
func TestDispatchCoalescesDuplicatesAndKeepsAggregateOrder(t *testing.T) {
	d := testenv.DB(t)
	ctx := context.Background()
	topic := fmt.Sprintf("test.%d", time.Now().UnixNano())
	if err := d.InTx(ctx, func(tx pgx.Tx) error {
		for i := 0; i < 5; i++ { // five identical "comments changed" on page 1
			if err := Publish(ctx, tx, topic, 1, CommentsEvent{ContentID: 1}); err != nil {
				return err
			}
		}
		for i := 1; i <= 3; i++ { // three distinct, ordered events on page 2
			if err := Publish(ctx, tx, topic, 2, map[string]int{"step": i}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	runs := map[int64][]string{}
	disp := NewDispatcher(d, 4)
	disp.On(topic, func(_ context.Context, e Event) error {
		mu.Lock()
		defer mu.Unlock()
		runs[e.AggregateID] = append(runs[e.AggregateID], string(e.Payload))
		return nil
	})
	drain(t, disp)
	if len(runs[1]) != 1 {
		t.Errorf("aggregate 1 handled %d times, want 1 (coalesced)", len(runs[1]))
	}
	// Payloads come back as Postgres prints jsonb (with a space after the colon).
	if got := strings.Join(runs[2], " "); got != `{"step": 1} {"step": 2} {"step": 3}` {
		t.Errorf("aggregate 2 order = %s", got)
	}
	assertPending(t, d, topic, 0)
}

func TestAssignLanesKeepsOneAggregateOnOneLane(t *testing.T) {
	disp := NewDispatcher(nil, 3)
	var groups []*group
	for i := 0; i < 30; i++ {
		groups = append(groups, &group{event: Event{AggregateID: int64(i % 5)}})
	}
	seen := map[int64]int{}
	for lane, gs := range disp.assignLanes(groups) {
		for _, g := range gs {
			if prev, ok := seen[g.event.AggregateID]; ok && prev != lane {
				t.Fatalf("aggregate %d on lanes %d and %d", g.event.AggregateID, prev, lane)
			}
			seen[g.event.AggregateID] = lane
		}
	}
}
