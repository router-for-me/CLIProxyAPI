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

	CacheClaudeWebSearchResult("session-a", "srvtoolu_1", []byte(`[{"url":"a"}]`))

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

	CacheClaudeWebSearchResult("s", "srvtoolu_1", []byte(`[1]`))
	now = now.Add(claudeWebSearchResultTTL - time.Minute)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1"); !ok {
		t.Fatal("entry expired before its TTL")
	}
	// The read above extended the lifetime.
	now = now.Add(claudeWebSearchResultTTL - time.Minute)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1"); !ok {
		t.Fatal("read did not extend the entry lifetime")
	}
	now = now.Add(claudeWebSearchResultTTL)
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1"); ok {
		t.Fatal("entry survived past its TTL")
	}
	if claudeWebSearchResultBytes != 0 || claudeWebSearchResultOrder.Len() != 0 {
		t.Fatalf("expired entry not removed: bytes=%d len=%d", claudeWebSearchResultBytes, claudeWebSearchResultOrder.Len())
	}
}

func TestClaudeWebSearchResultCacheEnforcesByteBudget(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	CacheClaudeWebSearchResult("s", "too-big", make([]byte, claudeWebSearchResultMaxEntryBytes+1))
	if _, ok := GetClaudeWebSearchResult("s", "too-big"); ok {
		t.Fatal("entry above the per-entry limit was stored")
	}

	chunk := make([]byte, claudeWebSearchResultMaxEntryBytes)
	count := claudeWebSearchResultMaxBytes/claudeWebSearchResultMaxEntryBytes + 1
	for i := 0; i < count; i++ {
		CacheClaudeWebSearchResult("s", string(rune('a'+i)), chunk)
	}
	if claudeWebSearchResultBytes > claudeWebSearchResultMaxBytes {
		t.Fatalf("byte budget exceeded: %d > %d", claudeWebSearchResultBytes, claudeWebSearchResultMaxBytes)
	}
	if _, ok := GetClaudeWebSearchResult("s", "a"); ok {
		t.Fatal("least recently used entry was not evicted")
	}
	if _, ok := GetClaudeWebSearchResult("s", string(rune('a'+count-1))); !ok {
		t.Fatal("newest entry was evicted")
	}

	CacheClaudeWebSearchResult("s", "b", []byte(`[2]`))
	if got, _ := GetClaudeWebSearchResult("s", "b"); string(got) != `[2]` {
		t.Fatalf("overwrite = %q, want [2]", got)
	}
}

// Long client session identifiers must not let small entries grow the store
// past its limits: keys are hashed and every entry is charged its overhead.
func TestClaudeWebSearchResultCacheBoundsManySmallEntriesWithLongKeys(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	longSession := strings.Repeat("s", 4096)
	for i := 0; i < claudeWebSearchResultMaxEntries+100; i++ {
		CacheClaudeWebSearchResult(longSession+strconv.Itoa(i), "srvtoolu_"+strconv.Itoa(i), []byte(`[1]`))
	}
	if got := claudeWebSearchResultOrder.Len(); got != claudeWebSearchResultMaxEntries {
		t.Fatalf("entries = %d, want %d", got, claudeWebSearchResultMaxEntries)
	}
	keyBytes := 0
	for key := range claudeWebSearchResultIndex {
		keyBytes += len(key)
	}
	if keyBytes > claudeWebSearchResultMaxEntries*64 {
		t.Fatalf("stored key bytes = %d, want fixed-size keys", keyBytes)
	}
	if claudeWebSearchResultBytes > claudeWebSearchResultMaxBytes {
		t.Fatalf("byte budget exceeded: %d", claudeWebSearchResultBytes)
	}
	last := claudeWebSearchResultMaxEntries + 99
	if _, ok := GetClaudeWebSearchResult(longSession+strconv.Itoa(last), "srvtoolu_"+strconv.Itoa(last)); !ok {
		t.Fatal("newest entry was evicted")
	}
	if _, ok := GetClaudeWebSearchResult(longSession+"0", "srvtoolu_0"); ok {
		t.Fatal("oldest entry survived the entry limit")
	}
}
