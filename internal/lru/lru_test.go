package lru

import (
	"testing"
)

func TestLRUEvictsByBytes(t *testing.T) {
	c := New(100, func(b []byte) int64 { return int64(len(b)) })
	c.Put("a", make([]byte, 20))
	c.Put("b", make([]byte, 20))
	c.Get("a") // a is now most recent
	for i := range 4 {
		c.Put(string(rune('c'+i)), make([]byte, 20))
	}
	if _, ok := c.Get("b"); ok {
		t.Error("b should have been evicted first")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a was recently used and should survive")
	}
	c.Put("huge", make([]byte, 60)) // > max/4: not cached at all
	if _, ok := c.Get("huge"); ok {
		t.Error("oversized entry must not be cached")
	}
}
