package submission

import (
	"container/list"
	"strconv"
	"sync"

	"golang.org/x/sync/singleflight"

	"code-sprout-cms/internal/judge"
)

// caseCache keeps recently used test sets in memory, bounded by total bytes rather than entry
// count: sets range from a few KB to hundreds of MB, so a count limit could not bound memory.
// Test sets are immutable (a new upload is a new version/id), so entries never go stale.
// Concurrent misses for the same set are collapsed into one load.
type caseCache struct {
	mu       sync.Mutex
	budget   int
	used     int
	order    *list.List // front = most recently used
	entries  map[int64]*list.Element
	inflight singleflight.Group
}

type cacheEntry struct {
	id    int64
	cases []judge.TestCase
	bytes int
}

func newCaseCache(budgetBytes int) *caseCache {
	return &caseCache{budget: budgetBytes, order: list.New(), entries: map[int64]*list.Element{}}
}

func setBytes(cases []judge.TestCase) int {
	n := 0
	for _, c := range cases {
		n += len(c.Stdin) + len(c.Expected)
	}
	return n
}

// get returns the cases for test set id, calling load on a miss.
func (c *caseCache) get(id int64, load func() ([]judge.TestCase, error)) ([]judge.TestCase, error) {
	c.mu.Lock()
	if el, ok := c.entries[id]; ok {
		c.order.MoveToFront(el)
		cases := el.Value.(*cacheEntry).cases
		c.mu.Unlock()
		return cases, nil
	}
	c.mu.Unlock()

	v, err, _ := c.inflight.Do(strconv.FormatInt(id, 10), func() (any, error) { return load() })
	if err != nil {
		return nil, err
	}
	cases := v.([]judge.TestCase)
	c.put(id, cases)
	return cases, nil
}

func (c *caseCache) put(id int64, cases []judge.TestCase) {
	size := setBytes(cases)
	if size > c.budget {
		return // larger than the whole cache: serve it but do not keep it
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[id]; ok {
		return
	}
	for c.used+size > c.budget && c.order.Len() > 0 {
		oldest := c.order.Back()
		e := oldest.Value.(*cacheEntry)
		c.order.Remove(oldest)
		delete(c.entries, e.id)
		c.used -= e.bytes
	}
	c.entries[id] = c.order.PushFront(&cacheEntry{id: id, cases: cases, bytes: size})
	c.used += size
}
