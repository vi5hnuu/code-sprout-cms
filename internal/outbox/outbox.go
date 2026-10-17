// Package outbox implements the transactional outbox: domain code records an event in the same
// transaction as the change it describes, and the worker delivers it to handlers afterwards.
// This is what keeps search, sitemaps, caches and ISR in step with the database without
// dual writes; if the transaction rolls back, the event never existed.
//
// Delivery is at-least-once, so every handler must be idempotent (re-indexing a document,
// recomputing a sitemap chunk and purging a cache key all are).
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"code-sprout-cms/internal/outbox/outboxdb"
)

// Topics. Named <aggregate>.<past-tense verb>; payloads are documented beside each constant.
const (
	TopicContentPublished   = "content.published"   // ContentEvent
	TopicContentUnpublished = "content.unpublished" // ContentEvent
	TopicContentUpdated     = "content.updated"     // ContentEvent: live content re-published
	TopicCategoryChanged    = "category.changed"    // CategoryEvent: created, edited, moved or deleted
	TopicTagChanged         = "tag.changed"         // TagEvent
)

// CategoryEvent carries the affected paths: a move invalidates pages under both the old and
// the new location.
type CategoryEvent struct {
	Paths []string `json:"paths"`
}

type TagEvent struct {
	Kind string `json:"kind"`
	Slug string `json:"slug"`
}

// ContentEvent is deliberately small: handlers re-read current state instead of trusting a
// snapshot, so out-of-order or repeated delivery still converges to the right result.
type ContentEvent struct {
	Type   string `json:"type"`
	Locale string `json:"locale"`
	Slug   string `json:"slug"`
}

// Publish records an event. db must be the transaction that performs the change.
func Publish(ctx context.Context, db outboxdb.DBTX, topic string, aggregateID int64, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox %s: marshal: %w", topic, err)
	}
	return outboxdb.New(db).InsertEvent(ctx, outboxdb.InsertEventParams{Topic: topic, AggregateID: aggregateID, Payload: body})
}
