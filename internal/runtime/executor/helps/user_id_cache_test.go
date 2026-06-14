package helps

import (
	"context"
	"errors"
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

	first := CachedUserID("api-key-1")
	second := CachedUserID("api-key-1")

	if first == "" {
		t.Fatal("expected generated user_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached user_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedUserID_ExpiresAfterTTL(t *testing.T) {
	resetUserIDCache()

	expiredID := CachedUserID("api-key-expired")
	cacheKey := userIDCacheKey("api-key-expired")
	userIDCacheMu.Lock()
	userIDCache[cacheKey] = userIDCacheEntry{
		value:  expiredID,
		expire: time.Now().Add(-time.Minute),
	}
	userIDCacheMu.Unlock()

	newID := CachedUserID("api-key-expired")
	if newID == expiredID {
		t.Fatalf("expected expired user_id to be replaced, got %q", newID)
	}
	if newID == "" {
		t.Fatal("expected regenerated user_id to be non-empty")
	}
}

func TestCachedUserID_IsScopedByAPIKey(t *testing.T) {
	resetUserIDCache()

	first := CachedUserID("api-key-1")
	second := CachedUserID("api-key-2")

	if first == second {
		t.Fatalf("expected different API keys to have different user_ids, got %q", first)
	}
}

func TestCachedUserID_RenewsTTLOnHit(t *testing.T) {
	resetUserIDCache()

	key := "api-key-renew"
	id := CachedUserID(key)
	cacheKey := userIDCacheKey(key)

	soon := time.Now()
	userIDCacheMu.Lock()
	userIDCache[cacheKey] = userIDCacheEntry{
		value:  id,
		expire: soon.Add(2 * time.Second),
	}
	userIDCacheMu.Unlock()

	if refreshed := CachedUserID(key); refreshed != id {
		t.Fatalf("expected cached user_id to be reused before expiry, got %q", refreshed)
	}

	userIDCacheMu.RLock()
	entry := userIDCache[cacheKey]
	userIDCacheMu.RUnlock()

	if entry.expire.Sub(soon) < 30*time.Minute {
		t.Fatalf("expected TTL to renew, got %v remaining", entry.expire.Sub(soon))
	}
}

func TestCachedUserIDRequiredHomeReusesKVAcrossLocalCacheReset(t *testing.T) {
	resetUserIDCache()
	client := newFakeClaudeIDKVClient()
	useFakeClaudeIDKVClient(t, client, true, nil)

	first, errFirst := CachedUserIDRequired(context.Background(), "api-key-1")
	if errFirst != nil {
		t.Fatalf("CachedUserIDRequired() first error = %v", errFirst)
	}
	resetUserIDCache()
	second, errSecond := CachedUserIDRequired(context.Background(), "api-key-1")
	if errSecond != nil {
		t.Fatalf("CachedUserIDRequired() second error = %v", errSecond)
	}
	if first != second {
		t.Fatalf("user id = %q then %q, want same Home KV value", first, second)
	}
	if !IsValidUserID(first) {
		t.Fatalf("user id %q is not valid", first)
	}
	if client.setCount != 1 {
		t.Fatalf("KVSetNX count = %d, want 1", client.setCount)
	}
	if client.expireCount != 1 || client.lastExpireTTL != userIDTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, userIDTTL)
	}
	if client.lastSetTTL != userIDTTL {
		t.Fatalf("KVSetNX ttl = %v, want %v", client.lastSetTTL, userIDTTL)
	}
}

func TestCachedUserIDRequiredEmptyAPIKeyDoesNotUseHomeKV(t *testing.T) {
	client := newFakeClaudeIDKVClient()
	useFakeClaudeIDKVClient(t, client, true, nil)

	value, errValue := CachedUserIDRequired(context.Background(), "")
	if errValue != nil {
		t.Fatalf("CachedUserIDRequired(empty) error = %v", errValue)
	}
	if !IsValidUserID(value) {
		t.Fatalf("user id %q is not valid", value)
	}
	if client.getCount != 0 || client.setCount != 0 || client.expireCount != 0 {
		t.Fatalf("KV calls = get %d set %d expire %d, want all zero", client.getCount, client.setCount, client.expireCount)
	}
}

func TestCachedUserIDRequiredHomeKVFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *fakeClaudeIDKVClient
	}{
		{name: "get", client: &fakeClaudeIDKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "set", client: &fakeClaudeIDKVClient{values: make(map[string][]byte), setErr: errors.New("set failed")}},
		{name: "expire", client: &fakeClaudeIDKVClient{values: map[string][]byte{
			claudeUserIDKVKey("api-key-1"): []byte(GenerateFakeUserID()),
		}, expireErr: errors.New("expire failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeClaudeIDKVClient(t, tc.client, true, nil)
			if _, errValue := CachedUserIDRequired(context.Background(), "api-key-1"); errValue == nil {
				t.Fatalf("CachedUserIDRequired() error = nil, want error")
			}
		})
	}
}

func TestCachedUserIDRequiredHomeRequiresReadAfterSet(t *testing.T) {
	client := newFakeClaudeIDKVClient()
	client.setNoPersist = true
	useFakeClaudeIDKVClient(t, client, true, nil)

	if _, errValue := CachedUserIDRequired(context.Background(), "api-key-1"); errValue == nil {
		t.Fatalf("CachedUserIDRequired() error = nil, want missing-after-set error")
	}
}
