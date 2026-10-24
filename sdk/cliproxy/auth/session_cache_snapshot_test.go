package auth

import (
	"testing"
	"time"
)

func TestSessionCacheSnapshotRestorePreservesAliasesAndExpiry(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	cache.SetAliases("auth-b", "provider::session::model", "provider::fallback::model")

	snapshot := cache.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot entries = %d, want 1", len(snapshot))
	}

	restored := NewSessionCache(time.Hour)
	defer restored.Stop()
	if got := restored.Restore(snapshot); got != 1 {
		t.Fatalf("restored entries = %d, want 1", got)
	}
	for _, alias := range snapshot[0].Aliases {
		if authID, ok := restored.Get(alias); !ok || authID != "auth-b" {
			t.Fatalf("restored alias %q = %q, %v", alias, authID, ok)
		}
	}
}

func TestSessionCacheRestoreDropsExpiredEntries(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	got := cache.Restore([]SessionCacheSnapshotEntry{{
		AuthID: "auth-a", Aliases: []string{"expired"}, ExpiresAt: time.Now().Add(-time.Second),
	}})
	if got != 0 {
		t.Fatalf("restored expired entries = %d, want 0", got)
	}
}

func TestSessionCacheRestoreCapsExpiryAtCurrentTTL(t *testing.T) {
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()
	far := time.Now().Add(24 * time.Hour)
	if got := cache.Restore([]SessionCacheSnapshotEntry{{AuthID: "auth-a", Aliases: []string{"s1"}, ExpiresAt: far}}); got != 1 {
		t.Fatalf("restored entries = %d, want 1", got)
	}
	snapshot := cache.Snapshot()
	if len(snapshot) != 1 || snapshot[0].ExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("restored expiry not capped at the current TTL: %#v", snapshot)
	}
}

func TestSessionCacheSnapshotKeepsEvictionRecency(t *testing.T) {
	cache := NewSessionCache(time.Hour)
	defer cache.Stop()
	cache.Set("a-older", "auth-a")
	cache.Set("z-newer", "auth-b")
	cache.GetAndRefresh("a-older") // now the most recently used

	snapshot := cache.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Aliases[0] != "z-newer" || snapshot[1].Aliases[0] != "a-older" {
		t.Fatalf("snapshot order = %#v, want least recently used first", snapshot)
	}

	// With room for one more group, restoring and adding a new binding must evict
	// the least recently used group, not the alphabetically first one.
	restored := NewSessionCacheWithCapacity(time.Hour, 2)
	defer restored.Stop()
	restored.Restore(snapshot)
	restored.Set("new", "auth-c")
	if _, ok := restored.Get("a-older"); !ok {
		t.Fatal("most recently used group was evicted after restore")
	}
	if _, ok := restored.Get("z-newer"); ok {
		t.Fatal("least recently used group survived eviction after restore")
	}
}

func TestSessionCacheSnapshotKeepsOrderWhenExpiriesTie(t *testing.T) {
	// Restoring under a lower TTL caps every expiry to the same instant; the next
	// snapshot must still follow eviction order rather than alias order.
	cache := NewSessionCache(time.Minute)
	defer cache.Stop()
	far := time.Now().Add(24 * time.Hour)
	cache.Restore([]SessionCacheSnapshotEntry{
		{AuthID: "auth-b", Aliases: []string{"z-least-recent"}, ExpiresAt: far},
		{AuthID: "auth-a", Aliases: []string{"a-most-recent"}, ExpiresAt: far},
	})
	snapshot := cache.Snapshot()
	if len(snapshot) != 2 || snapshot[0].Aliases[0] != "z-least-recent" || snapshot[1].Aliases[0] != "a-most-recent" {
		t.Fatalf("snapshot order = %#v, want restore order", snapshot)
	}
}
