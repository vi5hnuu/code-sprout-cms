package search

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
	"unicode/utf8"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/problem"
)

// maxBodyChars bounds each document's body. The opening of an article carries nearly all of its
// search value, and a bounded size keeps the index (and its RAM/disk) proportional to item
// count rather than to article length.
const maxBodyChars = 20_000

// reindexChunk is how many items one reindex event handles before chaining the next, so a full
// rebuild of millions of items never outlives an outbox lease.
const reindexChunk = 2000

type Source interface {
	Indexable(ctx context.Context, id int64) (content.Indexable, bool, error)
	LiveIDsAfter(ctx context.Context, after int64, limit int32) ([]int64, error)
	LiveIDsInCategoryAfter(ctx context.Context, categoryID, after int64, limit int32) ([]int64, error)
	LiveIDsWithTagAfter(ctx context.Context, tagID, after int64, limit int32) ([]int64, error)
}

type Difficulties interface {
	Difficulties(ctx context.Context, ids []int64) (map[int64]problem.Difficulty, error)
}

type Indexer struct {
	index Index
	src   Source
	diffs Difficulties
	// enqueue publishes a follow-up event (the next reindex chunk).
	enqueue func(ctx context.Context, topic string, aggregateID int64, payload any) error
}

func NewIndexer(index Index, src Source, diffs Difficulties, enqueue func(ctx context.Context, topic string, aggregateID int64, payload any) error) *Indexer {
	return &Indexer{index: index, src: src, diffs: diffs, enqueue: enqueue}
}

// Subscriber is where the indexer registers its event handlers (the outbox dispatcher).
type Subscriber interface {
	On(topic string, h outbox.Handler)
}

// Register subscribes the indexer to the events that change what is searchable.
func (ix *Indexer) Register(d Subscriber) {
	sync := func(ctx context.Context, e outbox.Event) error { return ix.Sync(ctx, []int64{e.AggregateID}) }
	d.On(outbox.TopicContentPublished, sync)
	d.On(outbox.TopicContentUpdated, sync)
	d.On(outbox.TopicContentUnpublished, sync)
	d.On(outbox.TopicSearchReindex, ix.reindexChunk)
	// A renamed or moved category changes the path and name every item under it carries.
	d.On(outbox.TopicCategoryChanged, func(ctx context.Context, e outbox.Event) error {
		return ix.reindexCategoryChunk(ctx, scopeCursor{CategoryID: e.AggregateID})
	})
	d.On(outbox.TopicSearchReindexCategory, ix.onScopeChunk(ix.reindexCategoryChunk))
	// A renamed tag changes the tag names its items carry. A deleted tag has already lost its
	// items (content_tag cascades), so its walk ends at once; their documents shed the name the
	// next time they are synced.
	d.On(outbox.TopicTagChanged, func(ctx context.Context, e outbox.Event) error {
		return ix.reindexTagChunk(ctx, scopeCursor{TagID: e.AggregateID})
	})
	d.On(outbox.TopicSearchReindexTag, ix.onScopeChunk(ix.reindexTagChunk))
}

// scopeCursor is the position of a re-index walk over one category's or one tag's items.
type scopeCursor struct {
	CategoryID int64 `json:"categoryId,omitempty"`
	TagID      int64 `json:"tagId,omitempty"`
	After      int64 `json:"after"`
}

func (ix *Indexer) onScopeChunk(run func(context.Context, scopeCursor) error) outbox.Handler {
	return func(ctx context.Context, e outbox.Event) error {
		var cur scopeCursor
		if err := json.Unmarshal(e.Payload, &cur); err != nil {
			return err
		}
		return run(ctx, cur)
	}
}

// reindexCategoryChunk re-syncs one chunk of a category subtree's items and chains the next, so
// a category with millions of items is handled in bounded steps. A deleted category has no
// items left (deletion requires an empty category), so its walk ends at once.
func (ix *Indexer) reindexCategoryChunk(ctx context.Context, cur scopeCursor) error {
	ids, err := ix.src.LiveIDsInCategoryAfter(ctx, cur.CategoryID, cur.After, reindexChunk)
	if err != nil {
		return err
	}
	return ix.syncAndChain(ctx, ids, outbox.TopicSearchReindexCategory, cur.CategoryID, cur)
}

// reindexTagChunk is reindexCategoryChunk for the items carrying one tag.
func (ix *Indexer) reindexTagChunk(ctx context.Context, cur scopeCursor) error {
	ids, err := ix.src.LiveIDsWithTagAfter(ctx, cur.TagID, cur.After, reindexChunk)
	if err != nil {
		return err
	}
	return ix.syncAndChain(ctx, ids, outbox.TopicSearchReindexTag, cur.TagID, cur)
}

// syncAndChain syncs one chunk and, when it was full, enqueues the walk's next chunk.
func (ix *Indexer) syncAndChain(ctx context.Context, ids []int64, topic string, aggregateID int64, cur scopeCursor) error {
	if err := ix.Sync(ctx, ids); err != nil {
		return err
	}
	if len(ids) < reindexChunk {
		return nil
	}
	cur.After = ids[len(ids)-1]
	return ix.enqueue(ctx, topic, aggregateID, cur)
}

// EnsureSettings applies the index settings (filterable and searchable attributes) when the
// worker starts, retrying with backoff until search is reachable. Without them a fresh index
// rejects every filtered query; applying identical settings again is a no-op for the engine.
func (ix *Indexer) EnsureSettings(ctx context.Context) {
	for attempt := int32(1); ; attempt++ {
		err := ix.index.Configure(ctx)
		if err == nil {
			return
		}
		wait := outbox.Backoff(attempt)
		slog.Warn("search settings not applied; retrying", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// Sync makes the index match the database for these items: live public ones are upserted, all
// others removed. Re-reading current state (rather than trusting the event) keeps it correct
// under repeated or out-of-order delivery.
func (ix *Indexer) Sync(ctx context.Context, ids []int64) error {
	var docs []Document
	var gone, problems []int64
	for _, id := range ids {
		it, ok, err := ix.src.Indexable(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			gone = append(gone, id)
			continue
		}
		if it.Type == content.TypeProblem {
			problems = append(problems, id)
		}
		docs = append(docs, toDocument(it))
	}
	if len(problems) > 0 {
		diffs, err := ix.diffs.Difficulties(ctx, problems)
		if err != nil {
			return err
		}
		for i := range docs {
			if d, ok := diffs[docs[i].ID]; ok {
				docs[i].Difficulty = string(d)
			}
		}
	}
	if err := ix.index.Upsert(ctx, docs); err != nil {
		return err
	}
	return ix.index.Delete(ctx, gone)
}

func toDocument(it content.Indexable) Document {
	body := it.Text
	if utf8.RuneCountInString(body) > maxBodyChars {
		body = string([]rune(body)[:maxBodyChars])
	}
	tags := it.Tags
	if tags == nil {
		tags = []string{}
	}
	return Document{
		ID: it.ID, Type: string(it.Type), Locale: it.Locale, Slug: it.Slug, Title: it.Title, Summary: it.Summary,
		Body: body, Tags: tags, Category: it.CategoryPath, CategoryName: it.CategoryName, Access: it.Access, PublishedAt: it.PublishedAt.Unix(),
	}
}

type reindexCursor struct {
	After int64 `json:"after"`
}

// RequestReindex starts a full rebuild (after a schema change, a new engine, or bulk tag
// renames). It runs as a chain of small events processed by the worker.
func (ix *Indexer) RequestReindex(ctx context.Context) error {
	return ix.enqueue(ctx, outbox.TopicSearchReindex, 0, reindexCursor{})
}

func (ix *Indexer) reindexChunk(ctx context.Context, e outbox.Event) error {
	var cur reindexCursor
	if err := json.Unmarshal(e.Payload, &cur); err != nil {
		return err
	}
	if cur.After == 0 {
		if err := ix.index.Configure(ctx); err != nil {
			return err
		}
	}
	ids, err := ix.src.LiveIDsAfter(ctx, cur.After, reindexChunk)
	if err != nil {
		return err
	}
	if err := ix.Sync(ctx, ids); err != nil {
		return err
	}
	if len(ids) < reindexChunk {
		slog.Info("search reindex complete")
		return nil
	}
	next := ids[len(ids)-1]
	return ix.enqueue(ctx, outbox.TopicSearchReindex, next, reindexCursor{After: next})
}
