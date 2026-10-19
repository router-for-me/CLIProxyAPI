package cache

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

func useClaudeWebSearchResultClock(t *testing.T, now *time.Time) {
	t.Helper()
	ClearClaudeWebSearchResultCache()
	previous := claudeWebSearchResultNow
	claudeWebSearchResultNow = func() time.Time { return *now }
	t.Cleanup(func() {
		claudeWebSearchResultNow = previous
		ClearClaudeWebSearchResultCache()
	})
}

func TestClaudeWebSearchResultCacheScopesBySessionAndToolUse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	CacheClaudeWebSearchResult("session-a", "srvtoolu_1", []byte(`[{"url":"a"}]`), 30*time.Minute, 16)

	got, ok := GetClaudeWebSearchResult("session-a", "srvtoolu_1")
	if !ok || string(got) != `[{"url":"a"}]` {
		t.Fatalf("Get = %q, %v; want stored content", got, ok)
	}
	if _, ok := GetClaudeWebSearchResult("session-b", "srvtoolu_1"); ok {
		t.Fatal("content leaked to another session")
	}
	if _, ok := GetClaudeWebSearchResult("session-a", "srvtoolu_2"); ok {
		t.Fatal("content returned for another tool use id")
	}
	if _, ok := GetClaudeWebSearchResult("", "srvtoolu_1"); ok {
		t.Fatal("empty session key must not match")
	}
	got[0] = 'X'
	if again, _ := GetClaudeWebSearchResult("session-a", "srvtoolu_1"); !bytes.Equal(again, []byte(`[{"url":"a"}]`)) {
		t.Fatalf("caller mutation changed the cache: %q", again)
	}
}

func TestClaudeWebSearchResultCacheExpiresAndSlides(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	ttl := 30 * time.Minute
	CacheClaudeWebSearchResult("s", "srvtoolu_1", []byte(`[1]`), ttl, 16)
	now = now.Add(ttl - time.Minute)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1", ttl); !ok {
		t.Fatal("entry expired before its TTL")
	}
	// The read above extended the lifetime.
	now = now.Add(ttl - time.Minute)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1", ttl); !ok {
		t.Fatal("read did not extend the entry lifetime")
	}
	now = now.Add(ttl)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1", ttl); ok {
		t.Fatal("entry survived past its TTL")
	}
	if claudeWebSearchResultOrder.Len() != 0 {
		t.Fatalf("expired entry not removed: len=%d", claudeWebSearchResultOrder.Len())
	}
}

func TestClaudeWebSearchResultCacheEnforcesEntryLimitWithoutByteCap(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	const limit = 4
	longSession := strings.Repeat("s", 4096)
	// Large payloads must not be rejected by a byte limit; only the entry limit applies.
	largeContent := bytes.Repeat([]byte("a"), 5<<20)
	CacheClaudeWebSearchResult("s", "large", largeContent, time.Hour, limit)
	if got, ok := GetClaudeWebSearchResult("s", "large"); !ok || len(got) != len(largeContent) {
		t.Fatalf("large entry len = %d, ok = %v; want %d, true", len(got), ok, len(largeContent))
	}

	for i := 0; i < limit+10; i++ {
		CacheClaudeWebSearchResult(longSession+strconv.Itoa(i), "srvtoolu_"+strconv.Itoa(i), []byte(`[1]`), time.Hour, limit)
	}
	if got := claudeWebSearchResultOrder.Len(); got != limit {
		t.Fatalf("entries = %d, want %d", got, limit)
	}
	for key := range claudeWebSearchResultIndex {
		if len(key) != 64 {
			t.Fatalf("stored key len = %d, want 64-char SHA-256 hex", len(key))
		}
	}
	last := limit + 9
	if _, ok := GetClaudeWebSearchResult(longSession+strconv.Itoa(last), "srvtoolu_"+strconv.Itoa(last)); !ok {
		t.Fatal("newest entry was evicted")
	}
	if _, ok := GetClaudeWebSearchResult(longSession+"0", "srvtoolu_0"); ok {
		t.Fatal("oldest entry survived the entry limit")
	}

	CacheClaudeWebSearchResult(longSession+strconv.Itoa(last), "srvtoolu_"+strconv.Itoa(last), []byte(`[2]`), time.Hour, limit)
	if got, _ := GetClaudeWebSearchResult(longSession+strconv.Itoa(last), "srvtoolu_"+strconv.Itoa(last)); string(got) != `[2]` {
		t.Fatalf("overwrite = %q, want [2]", got)
	}
}

func TestClaudeWebSearchResultCachePurgesExpiredBeforeEvictingLiveEntries(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	CacheClaudeWebSearchResult("s", "short", []byte(`[1]`), 5*time.Minute, 2)
	CacheClaudeWebSearchResult("s", "long", []byte(`[2]`), 30*time.Minute, 2)
	// Touch "short" so "long" is the LRU tail, then advance past "short"'s TTL.
	_, _ = GetClaudeWebSearchResult("s", "short", 5*time.Minute)
	now = now.Add(10 * time.Minute)

	CacheClaudeWebSearchResult("s", "new", []byte(`[3]`), 30*time.Minute, 2)
	if _, ok := GetClaudeWebSearchResult("s", "long"); !ok {
		t.Fatal("live entry was evicted while an expired entry could have been purged")
	}
	if _, ok := GetClaudeWebSearchResult("s", "new"); !ok {
		t.Fatal("new entry missing")
	}
}
