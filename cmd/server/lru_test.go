package main

import (
	"slices"
	"testing"
)

// lruKeys lists c's keys, most recently used first.
func lruKeys[K comparable, V any](c *lru[K, V]) []K {
	var out []K
	for el := c.ll.Front(); el != nil; el = el.Next() {
		out = append(out, el.Value.(*lruEntry[K, V]).key)
	}
	return out
}

func lruWant[K comparable, V any](t *testing.T, c *lru[K, V], want ...K) {
	t.Helper()
	if got := lruKeys(c); !slices.Equal(got, want) || c.Len() != len(want) || len(c.items) != len(want) {
		t.Fatalf("keys %v (len %d, map %d), want %v", got, c.Len(), len(c.items), want)
	}
}

func TestLRUEvictsLeastRecentlyUsed(t *testing.T) {
	c := newLRU[string, int](3, 0, nil)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3)
	lruWant(t, c, "c", "b", "a")
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Fatalf("get a = %v, %v", v, ok)
	}
	lruWant(t, c, "a", "c", "b")
	c.Put("d", 4) // b is least recently used
	lruWant(t, c, "d", "a", "c")
	if v, ok := c.Get("b"); ok || v != 0 {
		t.Fatalf("evicted b = %v, %v", v, ok)
	}
	c.Put("e", 5)
	c.Put("f", 6)
	lruWant(t, c, "f", "e", "d")
	c.Remove("e")
	c.Remove("missing")
	lruWant(t, c, "f", "d")
}

func TestLRUPeekDoesNotTouch(t *testing.T) {
	c := newLRU[int, string](2, 0, nil)
	c.Put(1, "one")
	c.Put(2, "two")
	if v, ok := c.Peek(1); !ok || v != "one" {
		t.Fatalf("peek 1 = %q, %v", v, ok)
	}
	if _, ok := c.Peek(3); ok {
		t.Fatal("peek of a missing key hit")
	}
	lruWant(t, c, 2, 1)
	c.Put(3, "three") // 1 is still the least recently used
	lruWant(t, c, 3, 2)
}

func TestLRUUpdateInPlace(t *testing.T) {
	c := newLRU[string, []byte](3, 100, func(k string, v []byte) int { return len(k) + len(v) })
	c.Put("a", make([]byte, 10))
	c.Put("b", make([]byte, 10))
	c.Put("c", make([]byte, 10))
	c.Put("a", make([]byte, 30)) // replaces, moves to the front, resizes
	lruWant(t, c, "a", "c", "b")
	if v, _ := c.Peek("a"); len(v) != 30 || c.size != 31+11+11 {
		t.Fatalf("a holds %d bytes, size %d", len(v), c.size)
	}
	c.Put("b", make([]byte, 1)) // shrinking evicts nothing
	lruWant(t, c, "b", "a", "c")
	if c.size != 2+31+11 {
		t.Fatalf("size %d", c.size)
	}
}

func TestLRUByteBudget(t *testing.T) {
	c := newLRU[string, []byte](100, 100, func(k string, v []byte) int { return len(k) + len(v) })
	for _, k := range []string{"a", "b", "c", "d"} {
		c.Put(k, make([]byte, 24)) // 25 bytes each: exactly the budget
	}
	lruWant(t, c, "d", "c", "b", "a")
	c.Get("a")
	c.Put("e", make([]byte, 49)) // 50 bytes: b and c go, oldest first
	lruWant(t, c, "e", "a", "d")
	if c.size != 50+25+25 {
		t.Fatalf("size %d", c.size)
	}
	// A value over the whole budget is not stored, flushes nothing, and
	// drops the value its key had.
	c.Put("d", make([]byte, 100))
	lruWant(t, c, "e", "a")
	if c.size != 75 {
		t.Fatalf("size %d after an oversized put", c.size)
	}
	// One that fits exactly evicts everything else.
	c.Put("f", make([]byte, 99))
	lruWant(t, c, "f")
	c.Remove("f")
	if c.size != 0 || c.Len() != 0 {
		t.Fatalf("empty cache: size %d, len %d", c.size, c.Len())
	}
}

func TestLRUHoldsAtLeastOne(t *testing.T) {
	c := newLRU[int, int](0, 0, nil)
	c.Put(1, 1)
	c.Put(2, 2)
	lruWant(t, c, 2)
}
