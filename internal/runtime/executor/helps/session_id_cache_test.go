package helps

import (
	"testing"
	"time"
)

func resetSessionIDCache() {
	sessionIDCacheStore.clear()
}

func TestCachedSessionID_ReusesWithinTTL(t *testing.T) {
	resetSessionIDCache()

	first := CachedSessionID("api-key-1")
	second := CachedSessionID("api-key-1")

	if first == "" {
		t.Fatal("expected generated session_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached session_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedSessionID_ExpiresAfterTTL(t *testing.T) {
	resetSessionIDCache()

	expiredID := CachedSessionID("api-key-expired")
	cacheKey := sessionIDCacheKey("api-key-expired")
	shard := sessionIDCacheStore.shardForKey(cacheKey)
	shard.mu.Lock()
	shard.entries[cacheKey] = sessionIDCacheEntry{
		value:  expiredID,
		expire: time.Now().Add(-time.Minute),
	}
	shard.mu.Unlock()

	newID := CachedSessionID("api-key-expired")
	if newID == expiredID {
		t.Fatalf("expected expired session_id to be replaced, got %q", newID)
	}
}

func TestCachedSessionID_RenewsTTLOnHit(t *testing.T) {
	resetSessionIDCache()

	key := "api-key-renew"
	sessionID := CachedSessionID(key)
	cacheKey := sessionIDCacheKey(key)
	shard := sessionIDCacheStore.shardForKey(cacheKey)
	soon := time.Now()

	shard.mu.Lock()
	shard.entries[cacheKey] = sessionIDCacheEntry{
		value:  sessionID,
		expire: soon.Add(2 * time.Second),
	}
	shard.mu.Unlock()

	if refreshed := CachedSessionID(key); refreshed != sessionID {
		t.Fatalf("expected cached session_id to be reused before expiry, got %q", refreshed)
	}

	shard.mu.RLock()
	entry := shard.entries[cacheKey]
	shard.mu.RUnlock()

	if entry.expire.Sub(soon) < 30*time.Minute {
		t.Fatalf("expected TTL to renew, got %v remaining", entry.expire.Sub(soon))
	}
}
