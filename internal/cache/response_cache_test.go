package cache

import (
	"testing"
	"time"
)

func TestResponseCacheKeyIsSensitiveToEveryField(t *testing.T) {
	base := ResponseCacheKey("zen", "https://x/v1/chat/completions", "m1", false, []byte(`{"a":1}`))
	cases := map[string]string{
		"provider": ResponseCacheKey("other", "https://x/v1/chat/completions", "m1", false, []byte(`{"a":1}`)),
		"url":      ResponseCacheKey("zen", "https://y/v1/chat/completions", "m1", false, []byte(`{"a":1}`)),
		"model":    ResponseCacheKey("zen", "https://x/v1/chat/completions", "m2", false, []byte(`{"a":1}`)),
		"stream":   ResponseCacheKey("zen", "https://x/v1/chat/completions", "m1", true, []byte(`{"a":1}`)),
		"payload":  ResponseCacheKey("zen", "https://x/v1/chat/completions", "m1", false, []byte(`{"a":2}`)),
	}
	for name, key := range cases {
		if key == base {
			t.Fatalf("expected %s change to alter cache key", name)
		}
	}
	if ResponseCacheKey("zen", "u", "m", false, []byte(`{"a":1}`)) != ResponseCacheKey("zen", "u", "m", false, []byte(`{"a":1}`)) {
		t.Fatal("expected identical inputs to produce identical keys")
	}
}

func TestResponseCacheStoreAndGet(t *testing.T) {
	c := NewResponseCache(time.Minute, 4, 1024)
	key := ResponseCacheKey("zen", "u", "m", false, []byte(`{"a":1}`))

	if _, ok := c.Get(key); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Store(key, []byte(`{"ok":true}`), false)
	entry, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit after store")
	}
	if string(entry.Payload) != `{"ok":true}` {
		t.Fatalf("unexpected payload: %s", entry.Payload)
	}
	if entry.Stream {
		t.Fatal("expected non-stream entry")
	}

	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 || stats.Stores != 1 || stats.Entries != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestResponseCacheStoreCopiesPayload(t *testing.T) {
	c := NewResponseCache(time.Minute, 4, 1024)
	key := "k"
	payload := []byte(`{"ok":true}`)
	c.Store(key, payload, false)
	payload[2] = 'X'

	entry, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit")
	}
	if string(entry.Payload) != `{"ok":true}` {
		t.Fatalf("cache retained aliased payload: %s", entry.Payload)
	}
}

func TestResponseCacheRejectsOversizedPayload(t *testing.T) {
	c := NewResponseCache(time.Minute, 4, 8)
	c.Store("k", []byte("0123456789"), false)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected oversized payload to be dropped")
	}
	if got := c.Stats().Stores; got != 0 {
		t.Fatalf("expected no stores, got %d", got)
	}
}

func TestResponseCacheExpiresEntries(t *testing.T) {
	c := NewResponseCache(time.Millisecond, 4, 1024)
	c.Store("k", []byte("v"), false)
	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected expired entry to miss")
	}
	if got := c.Stats().Entries; got != 0 {
		t.Fatalf("expected expired entry to be removed, entries=%d", got)
	}
}

func TestResponseCacheEvictsOldestBeyondCapacity(t *testing.T) {
	c := NewResponseCache(time.Minute, 2, 1024)
	c.Store("a", []byte("1"), false)
	c.Store("b", []byte("2"), false)
	c.Store("c", []byte("3"), false)

	if _, ok := c.Get("a"); ok {
		t.Fatal("expected oldest entry to be evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatal("expected b to survive")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("expected c to survive")
	}
	if got := c.Stats().Evictions; got != 1 {
		t.Fatalf("expected 1 eviction, got %d", got)
	}
}

func TestResponseCacheGetRefreshesRecency(t *testing.T) {
	c := NewResponseCache(time.Minute, 2, 1024)
	c.Store("a", []byte("1"), false)
	c.Store("b", []byte("2"), false)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected a to be cached")
	}
	c.Store("c", []byte("3"), false)

	if _, ok := c.Get("a"); !ok {
		t.Fatal("expected recently used a to survive eviction")
	}
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected least recently used b to be evicted")
	}
}

func TestResponseCachePurge(t *testing.T) {
	c := NewResponseCache(time.Minute, 4, 1024)
	c.Store("a", []byte("1"), false)
	c.Purge()
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected purge to drop entries")
	}
}

func TestResponseCacheNilSafe(t *testing.T) {
	var c *ResponseCache
	c.Store("a", []byte("1"), false)
	if _, ok := c.Get("a"); ok {
		t.Fatal("expected nil cache to miss")
	}
	if stats := c.Stats(); stats.Entries != 0 {
		t.Fatalf("expected zero stats, got %+v", stats)
	}
	c.Purge()
}

func TestResponseCacheModelAllowed(t *testing.T) {
	if !ResponseCacheModelAllowed(nil, "anything") {
		t.Fatal("expected empty allowlist to allow every model")
	}
	if !ResponseCacheModelAllowed([]string{"Claude-Opus-5"}, "claude-opus-5") {
		t.Fatal("expected case-insensitive match")
	}
	if ResponseCacheModelAllowed([]string{"claude-opus-5"}, "gpt-5.4-mini") {
		t.Fatal("expected non-listed model to be rejected")
	}
	if ResponseCacheModelAllowed([]string{"claude-opus-5"}, "  ") {
		t.Fatal("expected blank model to be rejected")
	}
}
