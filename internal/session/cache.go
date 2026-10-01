package session

import (
	"container/list"
	"sync"
)

// Why byte budgets, rather than a session count: one decoded tool-heavy
// transcript can exceed hundreds of ordinary sessions. Oversized reads are
// served normally but never admitted; eviction only drops cache ownership so
// in-flight readers keep their immutable snapshots.
const (
	EntriesCacheBytes         = 64 << 20
	maxSessionEntriesBytes    = 8 << 20
	TranscriptCacheBytes      = 16 << 20
	maxSessionTranscriptBytes = TranscriptCacheBytes
	maxCachedSessions         = 256
)

type cacheItem[T comparable] struct {
	key   string
	value T
	bytes int64
}

type weightedCache[T comparable] struct {
	mu                    sync.Mutex
	items                 map[string]*list.Element
	order                 list.List
	bytes, limit, perItem int64
}

func newWeightedCache[T comparable](limit, perItem int64) *weightedCache[T] {
	return &weightedCache[T]{items: map[string]*list.Element{}, limit: limit, perItem: perItem}
}

func (c *weightedCache[T]) get(key string, create func() T) T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.items[key]; e != nil {
		c.order.MoveToBack(e)
		return e.Value.(cacheItem[T]).value
	}
	v := create()
	c.items[key] = c.order.PushBack(cacheItem[T]{key: key, value: v})
	for len(c.items) > maxCachedSessions {
		c.remove(c.order.Front())
	}
	return v
}

func (c *weightedCache[T]) update(key string, value T, bytes int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.items[key]
	if e == nil || e.Value.(cacheItem[T]).value != value {
		return // an evicted reader cannot replace a newer snapshot
	}
	if bytes > c.perItem || bytes <= 0 {
		c.remove(e)
		return
	}
	item := e.Value.(cacheItem[T])
	c.bytes += bytes - item.bytes
	item.bytes = bytes
	e.Value = item
	c.order.MoveToBack(e)
	for c.bytes > c.limit {
		c.remove(c.order.Front())
	}
}

func (c *weightedCache[T]) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.items[key]; e != nil {
		c.remove(e)
	}
}

func (c *weightedCache[T]) remove(e *list.Element) {
	item := e.Value.(cacheItem[T])
	c.bytes -= item.bytes
	delete(c.items, item.key)
	c.order.Remove(e)
}
