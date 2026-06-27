package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/executor/helps"
)

func resetUserIDCache() {
	helps.ResetUserIDCacheForTest()
}

func TestCachedUserID_ReusesWithinTTL(t *testing.T) {
	resetUserIDCache()

	first := helps.CachedUserID("api-key-1")
	second := helps.CachedUserID("api-key-1")

	if first == "" {
		t.Fatal("expected generated user_id to be non-empty")
	}
	if first != second {
		t.Fatalf("expected cached user_id to be reused, got %q and %q", first, second)
	}
}

func TestCachedUserID_ExpiresAfterTTL(t *testing.T) {
	resetUserIDCache()

	expiredID := helps.CachedUserID("api-key-expired")
	helps.SetUserIDCacheEntryForTest("api-key-expired", expiredID, time.Now().Add(-time.Minute))

	newID := helps.CachedUserID("api-key-expired")
	if newID == expiredID {
		t.Fatalf("expected expired user_id to be replaced, got %q", newID)
	}
	if newID == "" {
		t.Fatal("expected regenerated user_id to be non-empty")
	}
}

func TestCachedUserID_IsScopedByAPIKey(t *testing.T) {
	resetUserIDCache()

	first := helps.CachedUserID("api-key-1")
	second := helps.CachedUserID("api-key-2")

	if first == second {
		t.Fatalf("expected different API keys to have different user_ids, got %q", first)
	}
}

func TestCachedUserID_RenewsTTLOnHit(t *testing.T) {
	resetUserIDCache()

	key := "api-key-renew"
	id := helps.CachedUserID(key)

	soon := time.Now()
	helps.SetUserIDCacheEntryForTest(key, id, soon.Add(2*time.Second))

	if refreshed := helps.CachedUserID(key); refreshed != id {
		t.Fatalf("expected cached user_id to be reused before expiry, got %q", refreshed)
	}

	expire, ok := helps.UserIDCacheEntryExpireForTest(key)
	if !ok {
		t.Fatal("expected cache entry")
	}

	if expire.Sub(soon) < 30*time.Minute {
		t.Fatalf("expected TTL to renew, got %v remaining", expire.Sub(soon))
	}
}

func TestUserIDCacheKey_DoesNotUseLegacySHA256(t *testing.T) {
	apiKey := "api-key-legacy-check"
	got := helps.UserIDCacheKeyForTest(apiKey)
	if got == "" {
		t.Fatal("expected non-empty cache key")
	}

	legacy := sha256.Sum256([]byte(apiKey))
	if got == hex.EncodeToString(legacy[:]) {
		t.Fatalf("expected cache key to differ from legacy sha256")
	}
}
