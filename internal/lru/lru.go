// Package lru provides a byte-bounded LRU cache.
package lru

import (
	"container/list"
	"sync"
)

// Cache is a byte-bounded LRU keyed by content hash, so entries never go stale.
type Cache[V any] struct {
	mu    sync.Mutex
	max   int64
	used  int64
	order *list.List // front = most recent
	items map[string]*list.Element
	size  func(V) int64
}

type entry[V any] struct {
	key string
	val V
	n   int64
}

func New[V any](maxBytes int64, size func(V) int64) *Cache[V] {
	return &Cache[V]{max: maxBytes, order: list.New(), items: map[string]*list.Element{}, size: size}
}

func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.order.MoveToFront(e)
		return e.Value.(*entry[V]).val, true
	}
	var zero V
	return zero, false
}

func (c *Cache[V]) Put(key string, val V) {
	n := c.size(val)
	if c.max <= 0 || n > c.max/4 { // disabled, or one entry would evict a quarter of the cache
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		c.used -= e.Value.(*entry[V]).n
		c.order.Remove(e)
	}
	c.items[key] = c.order.PushFront(&entry[V]{key, val, n})
	c.used += n
	for c.used > c.max {
		e := c.order.Back()
		ent := e.Value.(*entry[V])
		c.order.Remove(e)
		delete(c.items, ent.key)
		c.used -= ent.n
	}
}

func (c *Cache[V]) Bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
