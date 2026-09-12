package precompact

import (
	"testing"
	"time"
)

func TestCacheGetPutRoundTrip(t *testing.T) {
	c := NewCache(10, time.Hour)
	if _, ok := c.Get("sess-1"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Put("sess-1", Entry{Covered: 4, PrefixHash: "abc", Summary: "sum"})
	e, ok := c.Get("sess-1")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if e.Covered != 4 || e.PrefixHash != "abc" || e.Summary != "sum" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

func TestCacheExpires(t *testing.T) {
	c := NewCache(10, time.Millisecond)
	c.Put("sess-1", Entry{Covered: 1, PrefixHash: "x", Summary: "y"})
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("sess-1"); ok {
		t.Fatal("expected entry to expire")
	}
}

func TestCacheEmptySessionIsNoop(t *testing.T) {
	c := NewCache(10, time.Hour)
	c.Put("", Entry{Summary: "should not store"})
	if _, ok := c.Get(""); ok {
		t.Fatal("empty session key must never be stored")
	}
}
