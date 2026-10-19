package cache

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

type fakeClaudeWebSearchResultKVClient struct {
	values        map[string][]byte
	getErr        error
	setErr        error
	expireErr     error
	getCount      int
	setCount      int
	expireCount   int
	lastSetTTL    time.Duration
	lastExpireTTL time.Duration
}

func newFakeClaudeWebSearchResultKVClient() *fakeClaudeWebSearchResultKVClient {
	return &fakeClaudeWebSearchResultKVClient{values: make(map[string][]byte)}
}

func (c *fakeClaudeWebSearchResultKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return bytes.Clone(value), true, nil
}

func (c *fakeClaudeWebSearchResultKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = bytes.Clone(value)
	return true, nil
}

func (c *fakeClaudeWebSearchResultKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeClaudeWebSearchResultKVClient(t *testing.T, client *fakeClaudeWebSearchResultKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentClaudeWebSearchResultKVClient
	currentClaudeWebSearchResultKVClient = func() (claudeWebSearchResultKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentClaudeWebSearchResultKVClient = previous
	})
}

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

func TestClaudeWebSearchResultCacheHomeModeDoesNotUseLocalCache(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	CacheClaudeWebSearchResult("session-local", "srvtoolu_local", []byte(`[{"url":"local"}]`), 30*time.Minute, 16)

	homekv.SetCurrent(&homekv.Client{})
	t.Cleanup(homekv.ClearCurrent)

	if got, ok := GetClaudeWebSearchResult("session-local", "srvtoolu_local", 30*time.Minute); ok {
		t.Fatalf("GetClaudeWebSearchResult() = %q, true; want Home miss without local fallback", got)
	}

	CacheClaudeWebSearchResult("session-home", "srvtoolu_home", []byte(`[{"url":"home"}]`), 30*time.Minute, 16)
	homekv.ClearCurrent()

	if got, ok := GetClaudeWebSearchResult("session-home", "srvtoolu_home", 30*time.Minute); ok {
		t.Fatalf("local cache = %q, true; want empty after Home write failure", got)
	}
}

func TestClaudeWebSearchResultCacheHomeWriteReadAndSlidingExpire(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	client := newFakeClaudeWebSearchResultKVClient()
	useFakeClaudeWebSearchResultKVClient(t, client, true, nil)

	content := []byte(`[{"type":"web_search_result","url":"https://example.com","encrypted_content":"enc"}]`)
	if !CacheClaudeWebSearchResultBestEffort(context.Background(), "session-home", "srvtoolu_1", content, 45*time.Minute, 16) {
		t.Fatal("CacheClaudeWebSearchResultBestEffort() = false, want true")
	}
	if claudeWebSearchResultOrder.Len() != 0 {
		t.Fatalf("local cache len = %d, want 0 in Home mode", claudeWebSearchResultOrder.Len())
	}
	kvKey := claudeWebSearchResultKVKey("session-home", "srvtoolu_1")
	if client.setCount != 1 || client.lastSetTTL != 45*time.Minute {
		t.Fatalf("KVSet count/ttl = %d/%v, want 1/45m", client.setCount, client.lastSetTTL)
	}
	if !bytes.Equal(client.values[kvKey], content) {
		t.Fatalf("KV stored value = %q, want %q", client.values[kvKey], content)
	}

	got, ok, errGet := GetClaudeWebSearchResultRequired(context.Background(), "session-home", "srvtoolu_1", 30*time.Minute)
	if errGet != nil || !ok || !bytes.Equal(got, content) {
		t.Fatalf("GetClaudeWebSearchResultRequired() = %q, %v, %v; want %q, true, nil", got, ok, errGet, content)
	}
	if client.expireCount != 1 || client.lastExpireTTL != 30*time.Minute {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/30m", client.expireCount, client.lastExpireTTL)
	}
	got[0] = 'X'
	if !bytes.Equal(client.values[kvKey], content) {
		t.Fatalf("caller mutation altered Home KV fake state: %q", client.values[kvKey])
	}

	// Expire error should not discard a successfully fetched result.
	client.expireErr = errors.New("expire failed")
	gotAfterExpireErr, okAfterExpireErr, errAfterExpireErr := GetClaudeWebSearchResultRequired(context.Background(), "session-home", "srvtoolu_1")
	if errAfterExpireErr != nil || !okAfterExpireErr || !bytes.Equal(gotAfterExpireErr, content) {
		t.Fatalf("GetClaudeWebSearchResultRequired() with expireErr = %q, %v, %v; want %q, true, nil", gotAfterExpireErr, okAfterExpireErr, errAfterExpireErr, content)
	}
	if client.expireCount != 2 || client.lastExpireTTL != defaultClaudeWebSearchResultTTL {
		t.Fatalf("KVExpire default ttl = %d/%v, want 2/%v", client.expireCount, client.lastExpireTTL, defaultClaudeWebSearchResultTTL)
	}
}

func TestClaudeWebSearchResultCacheHomeRejectsEmptyScopeAndPropagatesErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	useClaudeWebSearchResultClock(t, &now)

	client := newFakeClaudeWebSearchResultKVClient()
	useFakeClaudeWebSearchResultKVClient(t, client, true, nil)

	if CacheClaudeWebSearchResultBestEffort(context.Background(), "", "srvtoolu_1", []byte(`[1]`), time.Minute, 16) {
		t.Fatal("empty session key should be rejected")
	}
	if CacheClaudeWebSearchResultBestEffort(context.Background(), "s", "", []byte(`[1]`), time.Minute, 16) {
		t.Fatal("empty toolUseID should be rejected")
	}
	if CacheClaudeWebSearchResultBestEffort(context.Background(), "s", "srvtoolu_1", nil, time.Minute, 16) {
		t.Fatal("empty content should be rejected")
	}
	if _, ok, errGet := GetClaudeWebSearchResultRequired(context.Background(), "", "srvtoolu_1"); errGet != nil || ok {
		t.Fatalf("empty session key Get = %v, %v; want false, nil", ok, errGet)
	}
	if client.getCount != 0 || client.setCount != 0 || client.expireCount != 0 {
		t.Fatalf("KV calls = get %d set %d expire %d, want all 0", client.getCount, client.setCount, client.expireCount)
	}

	client.setErr = errors.New("set failed")
	if CacheClaudeWebSearchResultBestEffort(context.Background(), "s", "srvtoolu_1", []byte(`[1]`), 0, 16) {
		t.Fatal("CacheClaudeWebSearchResultBestEffort() = true on setErr, want false")
	}
	if client.lastSetTTL != defaultClaudeWebSearchResultTTL {
		t.Fatalf("zero ttl defaulted to %v, want %v", client.lastSetTTL, defaultClaudeWebSearchResultTTL)
	}

	client.getErr = errors.New("get failed")
	if _, ok, errGet := GetClaudeWebSearchResultRequired(context.Background(), "s", "srvtoolu_1"); errGet == nil || ok {
		t.Fatalf("GetClaudeWebSearchResultRequired() on getErr = %v, %v; want false, error", ok, errGet)
	}
	if _, ok := GetClaudeWebSearchResult("s", "srvtoolu_1"); ok {
		t.Fatal("GetClaudeWebSearchResult() on getErr = true, want false")
	}
}
