package helps

import (
	"testing"
	"time"
)

func resetCodexCache() {
	codexCacheStore.clear()
}

func TestCodexCache_ReusesEntryBeforeExpiry(t *testing.T) {
	resetCodexCache()

	cache := CodexCache{
		ID:     "cache-id",
		Expire: time.Now().Add(time.Hour),
	}
	SetCodexCache("cache-key", cache)

	got, ok := GetCodexCache("cache-key")
	if !ok {
		t.Fatal("expected cached entry to exist")
	}
	if got != cache {
		t.Fatalf("GetCodexCache() = %+v, want %+v", got, cache)
	}
}

func TestCodexCache_RemovesExpiredEntryOnGet(t *testing.T) {
	resetCodexCache()

	key := "expired-cache-key"
	cache := CodexCache{
		ID:     "cache-id",
		Expire: time.Now().Add(-time.Minute),
	}
	SetCodexCache(key, cache)

	if got, ok := GetCodexCache(key); ok {
		t.Fatalf("expected expired cache miss, got %+v", got)
	}

	shard := codexCacheStore.shardForKey(key)
	shard.mu.RLock()
	_, exists := shard.entries[key]
	shard.mu.RUnlock()
	if exists {
		t.Fatal("expected expired codex cache entry to be deleted")
	}
}
