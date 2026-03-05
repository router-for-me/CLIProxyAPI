package executor

import (
	"testing"
	"time"
)

func resetUserIDCache() {
	userIDCacheMu.Lock()
	userIDCache = make(map[string]userIDCacheEntry)
	userIDCacheMu.Unlock()
}

func TestCachedUserID_ReusesWithinTTL(t *testing.T) {
	resetUserIDCache()

	first := cachedUserID("api-key-1")
	second := cachedUserID("api-key-1")

	if first == "" {
		t.Fatal("expected generated user_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached user_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedUserID_ExpiresAfterTTL(t *testing.T) {
	resetUserIDCache()

	expiredID := cachedUserID("api-key-expired")
	cacheKey := userIDCacheKey("api-key-expired")
	userIDCacheMu.Lock()
	userIDCache[cacheKey] = userIDCacheEntry{
		value:  expiredID,
		expire: time.Now().Add(-time.Minute),
	}
	userIDCacheMu.Unlock()

	newID := cachedUserID("api-key-expired")
	if newID == expiredID {
		t.Fatalf("expected expired user_id to be replaced, got %q", newID)
	}
	if newID == "" {
		t.Fatal("expected regenerated user_id to be non-empty")
	}
}

func TestCachedUserID_IsScopedByAPIKey(t *testing.T) {
	resetUserIDCache()

	first := cachedUserID("api-key-1")
	second := cachedUserID("api-key-2")

	if first == second {
		t.Fatalf("expected different API keys to have different user_ids, got %q", first)
	}
}

func TestCachedUserID_RenewsTTLOnHit(t *testing.T) {
	resetUserIDCache()

	key := "api-key-renew"
	id := cachedUserID(key)
	cacheKey := userIDCacheKey(key)

	soon := time.Now()
	userIDCacheMu.Lock()
	userIDCache[cacheKey] = userIDCacheEntry{
		value:  id,
		expire: soon.Add(2 * time.Second),
	}
	userIDCacheMu.Unlock()

	if refreshed := cachedUserID(key); refreshed != id {
		t.Fatalf("expected cached user_id to be reused before expiry, got %q", refreshed)
	}

	userIDCacheMu.RLock()
	entry := userIDCache[cacheKey]
	userIDCacheMu.RUnlock()

	if entry.expire.Sub(soon) < 30*time.Minute {
		t.Fatalf("expected TTL to renew, got %v remaining", entry.expire.Sub(soon))
	}
}
