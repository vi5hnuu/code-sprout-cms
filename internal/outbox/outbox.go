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
	"github.com/jackc/pgx/v5/pgxpool"

	"code-sprout-cms/internal/outbox/outboxdb"
)

// Topics. Named <aggregate>.<past-tense verb>; payloads are documented beside each constant.
const (
	TopicContentPublished   = "content.published"   // ContentEvent
	TopicContentUnpublished = "content.unpublished" // ContentEvent
	TopicContentUpdated     = "content.updated"     // ContentEvent: live content re-published
	TopicCategoryChanged    = "category.changed"    // CategoryEvent: created, edited, moved or deleted
	TopicTagChanged         = "tag.changed"         // TagEvent
	TopicCommentsChanged    = "comments.changed"    // CommentsEvent: a thread's public view changed
	TopicSearchReindex      = "search.reindex"      // {"after": id}: one chunk of a full rebuild
	// {"categoryId": id, "after": id}: one chunk of re-indexing a category's items after it was
	// renamed or moved (their documents carry its path and name).
	TopicSearchReindexCategory = "search.reindex_category"
)

type CommentsEvent struct {
	ContentID int64 `json:"contentId"`
}

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
	// OldSlug is set when a publish renamed the page, so its old URL is revalidated too.
	OldSlug string `json:"oldSlug,omitempty"`
}

// Publish records an event. db must be the transaction that performs the change.
func Publish(ctx context.Context, db outboxdb.DBTX, topic string, aggregateID int64, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox %s: marshal: %w", topic, err)
	}
	return outboxdb.New(db).InsertEvent(ctx, outboxdb.InsertEventParams{Topic: topic, AggregateID: aggregateID, Payload: body})
}

// Backlog reports events that are due but not yet handled, and the oldest one's wait in
// seconds: a growing backlog means the workers are behind or a downstream is failing.
func Backlog(ctx context.Context, pool *pgxpool.Pool) (due int64, oldestSeconds float64, err error) {
	row, err := outboxdb.New(pool).OutboxBacklog(ctx)
	return row.Due, row.OldestSeconds, err
}
