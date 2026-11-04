package cache

import (
	"fmt"
	"testing"
	"time"
)

func TestIssue5190InteractionsCacheUpdateAtCapacity(t *testing.T) {
	c := NewInteractionsSessionCache()
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	for i := 0; i < 1024; i++ {
		c.Put(fmt.Sprint(i), InteractionsContinuation{ID: "original"})
		now = now.Add(time.Second)
	}
	c.Put("1023", InteractionsContinuation{ID: "updated"})
	if len(c.entries) != 1024 {
		t.Fatalf("update evicted an unrelated entry: size=%d", len(c.entries))
	}
	if _, ok := c.Get("0"); !ok {
		t.Fatal("update evicted oldest entry")
	}
	if state, ok := c.Get("1023"); !ok || state.ID != "updated" {
		t.Fatalf("update not retained: %+v, %v", state, ok)
	}
}

func TestIssue5190InteractionsCacheExpiryAndBound(t *testing.T) {
	c := NewInteractionsSessionCache()
	now := time.Unix(100, 0)
	c.now = func() time.Time { return now }
	c.Put("first", InteractionsContinuation{ID: "one"})
	now = now.Add(30 * time.Minute)
	if _, ok := c.Get("first"); ok {
		t.Fatal("expired continuation retained")
	}
	for i := 0; i < 1030; i++ {
		c.Put(fmt.Sprint(i), InteractionsContinuation{ID: "one"})
		now = now.Add(time.Second)
	}
	if len(c.entries) != 1024 {
		t.Fatalf("size=%d", len(c.entries))
	}
	if _, ok := c.Get("0"); ok {
		t.Fatal("oldest continuation retained")
	}
	if InteractionsCallKey([]string{"b", "a"}) != InteractionsCallKey([]string{"a", "b"}) {
		t.Fatal("call order matters")
	}
	for _, calls := range [][]string{nil, {""}, {"a", "a"}, {"a\x00b"}} {
		if InteractionsCallKey(calls) != "" {
			t.Fatalf("accepted invalid calls %q", calls)
		}
	}
}
