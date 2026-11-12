package main

// lru is the in-memory least-recently-used map behind the planner's result
// caches (plannerLRU) and the SRTM tile cache (srtmSource).

import "container/list"

// lru maps keys to values, bounded by an item count and, when sizeOf is
// set, a byte budget. A Put past either bound evicts least recently used
// entries until the cache is within both. Expiry, if any, is the caller's.
// It is not safe for concurrent use: each caller guards it with its own
// mutex.
type lru[K comparable, V any] struct {
	maxItems int
	maxBytes int            // ignored when sizeOf is nil
	sizeOf   func(K, V) int // an entry's share of maxBytes; nil: no byte budget
	size     int            // sum of the entries' sizeOf
	ll       *list.List     // *lruEntry[K, V], most recently used first
	items    map[K]*list.Element
}

type lruEntry[K comparable, V any] struct {
	key  K
	val  V
	size int // sizeOf(key, val) when stored
}

// newLRU holds at most maxItems entries (at least one) and, when sizeOf is
// not nil, at most maxBytes of sizeOf in total.
func newLRU[K comparable, V any](maxItems, maxBytes int, sizeOf func(K, V) int) *lru[K, V] {
	return &lru[K, V]{maxItems: max(maxItems, 1), maxBytes: maxBytes, sizeOf: sizeOf,
		ll: list.New(), items: make(map[K]*list.Element)}
}

// Get returns k's value and marks it most recently used.
func (c *lru[K, V]) Get(k K) (V, bool) {
	el, ok := c.items[k]
	if !ok {
		var zero V
		return zero, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*lruEntry[K, V]).val, true
}

// Peek returns k's value without changing the eviction order.
func (c *lru[K, V]) Peek(k K) (V, bool) {
	el, ok := c.items[k]
	if !ok {
		var zero V
		return zero, false
	}
	return el.Value.(*lruEntry[K, V]).val, true
}

// Put stores v under k, replacing any value k had, as the most recently
// used entry, then evicts from the least recently used end until the cache
// is within its bounds. A value over the whole byte budget on its own is
// not stored, and drops any value k had, so it cannot flush the rest.
func (c *lru[K, V]) Put(k K, v V) {
	sz := 0
	if c.sizeOf != nil {
		if sz = c.sizeOf(k, v); sz > c.maxBytes {
			c.Remove(k)
			return
		}
	}
	if el, ok := c.items[k]; ok {
		e := el.Value.(*lruEntry[K, V])
		c.size += sz - e.size
		e.val, e.size = v, sz
		c.ll.MoveToFront(el)
	} else {
		c.items[k] = c.ll.PushFront(&lruEntry[K, V]{key: k, val: v, size: sz})
		c.size += sz
	}
	// k is at the front and fits on its own, so it is never evicted here.
	for c.ll.Len() > c.maxItems || (c.sizeOf != nil && c.size > c.maxBytes) {
		c.removeElement(c.ll.Back())
	}
}

// Remove drops k, if present.
func (c *lru[K, V]) Remove(k K) {
	if el, ok := c.items[k]; ok {
		c.removeElement(el)
	}
}

// Len is the number of entries.
func (c *lru[K, V]) Len() int { return c.ll.Len() }

func (c *lru[K, V]) removeElement(el *list.Element) {
	e := c.ll.Remove(el).(*lruEntry[K, V])
	delete(c.items, e.key)
	c.size -= e.size
}
