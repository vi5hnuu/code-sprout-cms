package search

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"code-sprout-cms/internal/content"
	"code-sprout-cms/internal/outbox"
	"code-sprout-cms/internal/problem"
)

type memIndex struct{ docs map[int64]Document }

func (m *memIndex) Configure(context.Context) error { return nil }
func (m *memIndex) Upsert(_ context.Context, docs []Document) error {
	for _, d := range docs {
		m.docs[d.ID] = d
	}
	return nil
}
func (m *memIndex) Delete(_ context.Context, ids []int64) error {
	for _, id := range ids {
		delete(m.docs, id)
	}
	return nil
}
func (m *memIndex) Search(context.Context, Query) (Result, error) { return Result{}, nil }

// memSource holds items 1..n, all filed under one category whose name can change.
type memSource struct {
	n        int64
	category string
}

func (s *memSource) Indexable(_ context.Context, id int64) (content.Indexable, bool, error) {
	if id < 1 || id > s.n {
		return content.Indexable{}, false, nil
	}
	return content.Indexable{ID: id, Type: content.TypeArticle, Slug: "a", Title: "A", CategoryPath: "dsa/arrays",
		CategoryName: s.category, PublishedAt: time.Unix(0, 0)}, true, nil
}

func (s *memSource) LiveIDsAfter(ctx context.Context, after int64, limit int32) ([]int64, error) {
	return s.LiveIDsInCategoryAfter(ctx, 7, after, limit)
}

func (s *memSource) LiveIDsInCategoryAfter(_ context.Context, _ int64, after int64, limit int32) ([]int64, error) {
	var ids []int64
	for id := after + 1; id <= s.n && len(ids) < int(limit); id++ {
		ids = append(ids, id)
	}
	return ids, nil
}

// handlers stands in for the outbox dispatcher: topic → handlers, run in order.
type handlers map[string][]outbox.Handler

func (h handlers) On(topic string, fn outbox.Handler) { h[topic] = append(h[topic], fn) }

func (h handlers) dispatch(ctx context.Context, e outbox.Event) error {
	for _, fn := range h[e.Topic] {
		if err := fn(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

type noDifficulties struct{}

func (noDifficulties) Difficulties(context.Context, []int64) (map[int64]problem.Difficulty, error) {
	return map[int64]problem.Difficulty{}, nil
}

// A renamed category re-indexes every item under it, in bounded chunks chained through the
// outbox, so the documents show the new name however large the category is.
func TestCategoryChangeReindexesItsItemsInChunks(t *testing.T) {
	ctx := context.Background()
	src := &memSource{n: reindexChunk*2 + 5, category: "Arrays"}
	idx := &memIndex{docs: map[int64]Document{}}
	var queued []outbox.Event
	ix := NewIndexer(idx, src, noDifficulties{}, func(_ context.Context, topic string, id int64, payload any) error {
		raw, _ := json.Marshal(payload)
		queued = append(queued, outbox.Event{Topic: topic, AggregateID: id, Payload: raw})
		return nil
	})
	d := handlers{}
	ix.Register(d)

	src.category = "Arrays & Hashing"
	if err := d.dispatch(ctx, outbox.Event{Topic: outbox.TopicCategoryChanged, AggregateID: 7}); err != nil {
		t.Fatal(err)
	}
	for len(queued) > 0 {
		e := queued[0]
		queued = queued[1:]
		if err := d.dispatch(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if int64(len(idx.docs)) != src.n {
		t.Fatalf("indexed %d documents, want %d", len(idx.docs), src.n)
	}
	for id, doc := range idx.docs {
		if doc.CategoryName != "Arrays & Hashing" {
			t.Fatalf("document %d still shows %q", id, doc.CategoryName)
		}
	}
}
