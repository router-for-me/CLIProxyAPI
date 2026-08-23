package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

type fakeClaudeThinkingReplayKVClient struct {
	values    map[string][]byte
	sets      int
	dels      int
	getErr    error
	setErr    error
	delErr    error
	swapErr   error
	swapsTTLs map[string]time.Duration
}

func newFakeClaudeThinkingReplayKVClient() *fakeClaudeThinkingReplayKVClient {
	return &fakeClaudeThinkingReplayKVClient{
		values:    make(map[string][]byte),
		swapsTTLs: make(map[string]time.Duration),
	}
}

func (c *fakeClaudeThinkingReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	v, ok := c.values[key]
	return append([]byte(nil), v...), ok, nil
}

func (c *fakeClaudeThinkingReplayKVClient) KVSet(_ context.Context, key string, value []byte, _ homekv.KVSetOptions) (bool, error) {
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	c.sets++
	return true, nil
}

func (c *fakeClaudeThinkingReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	if c.delErr != nil {
		return 0, c.delErr
	}
	var n int64
	for _, k := range keys {
		if _, ok := c.values[k]; ok {
			delete(c.values, k)
			n++
		}
	}
	c.dels += int(n)
	return n, nil
}

func (c *fakeClaudeThinkingReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, _ bool, newValue []byte, ttl time.Duration) (bool, error) {
	if c.swapErr != nil {
		return false, c.swapErr
	}
	current, ok := c.values[key]
	if !ok && expected == nil {
		c.values[key] = append([]byte(nil), newValue...)
		c.sets++
		c.swapsTTLs[key] = ttl
		return true, nil
	}
	if ok && string(current) == string(expected) {
		c.values[key] = append([]byte(nil), newValue...)
		c.sets++
		c.swapsTTLs[key] = ttl
		return true, nil
	}
	return false, nil
}

func (c *fakeClaudeThinkingReplayKVClient) KVExpire(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}

func useFakeClaudeThinkingReplayKVClient(t *testing.T, client kimiThinkingReplayKVClient, homeMode bool) {
	t.Helper()
	prev := currentClaudeThinkingReplayKVClient
	currentClaudeThinkingReplayKVClient = func() (kimiThinkingReplayKVClient, bool, error) {
		return client, homeMode, nil
	}
	t.Cleanup(func() {
		currentClaudeThinkingReplayKVClient = prev
		ClearClaudeThinkingReplayCache()
	})
}

func TestResolveClaudeThinkingReplayAliasScoresByWeightAndFirstUser(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"

	firstA := "firstA"
	firstB := "firstB"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg1", firstA)
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg2", firstA)
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionB", "msg1", firstB)

	// A request with first user A and messages [msg1, msg2] should resolve to A.
	msgs := []ClaudeThinkingReplayAliasMessage{{Hash: "msg1", Weight: 1}, {Hash: "msg2", Weight: 2}}
	if s, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, firstA); !ok || s != "sessionA" {
		t.Fatalf("resolve first A: got %q, want sessionA", s)
	}

	// Same messages with first user B should resolve to B, even though msg2
	// only belongs to A; msg1 is shared, but the first-user bonus for B tips
	// the scales.
	msgs = []ClaudeThinkingReplayAliasMessage{{Hash: "msg1", Weight: 1}}
	if s, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, firstB); !ok || s != "sessionB" {
		t.Fatalf("resolve first B: got %q, want sessionB", s)
	}
}

func TestResolveClaudeThinkingReplayAliasIgnoresExpiredEntries(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg1", "firstA")

	// Expire the alias by advancing time.
	claudeThinkingReplayAliasMu.Lock()
	for key, list := range claudeThinkingReplayAliases {
		for i := range list {
			list[i].timestamp = time.Now().Add(-2 * ClaudeThinkingReplayCacheTTL)
		}
		claudeThinkingReplayAliases[key] = list
	}
	claudeThinkingReplayAliasMu.Unlock()

	msgs := []ClaudeThinkingReplayAliasMessage{{Hash: "msg1", Weight: 1}}
	if _, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, "firstA"); ok {
		t.Fatal("expected no resolve for expired alias")
	}
}

func aliasValueIsLive(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return true
	}
	return len(value.Sessions) > 0
}

func TestClaudeThinkingReplayAliasHomeCappedPerCredential(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"

	// Register more than the per-credential cap and ensure the oldest keys
	// are evicted from the index.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max+10; i++ {
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHashFor(i), "first")
	}

	// The number of stored alias values should not exceed the cap.
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	index, _ := decodeClaudeThinkingReplayAliasIndex(client.values[indexKey])
	if len(index.Aliases) > max {
		t.Fatalf("credential alias cap exceeded: %d > %d", len(index.Aliases), max)
	}

	// The oldest entries should have been deleted or tombstoned.
	live := 0
	for k, v := range client.values {
		if k != indexKey && aliasValueIsLive(v) {
			live++
		}
	}
	if live > max+1 { // +1 for the index key itself
		t.Fatalf("too many live alias keys: %d", live)
	}
}

func TestClaudeThinkingReplayFallbackIndexDeletesOrphanRecords(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"

	// Seed a replay record for each fallback session so orphan detection can
	// observe real keys.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max+10; i++ {
		session := fmt.Sprintf("fb:session-%d", i)
		CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, session, []byte(`[{"type":"text","text":"x"}]`))
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, session, messageHashFor(i), "first")
	}

	// The per-credential alias cap should have evicted the oldest aliases and
	// the fallback index should have deleted their orphaned replay records.
	replays := 0
	for k := range client.values {
		if strings.HasPrefix(k, "cpa:claude:thinking-replay:") {
			replays++
		}
	}
	fallbackCap := ClaudeThinkingReplayCacheMaxFallbackSessions
	if replays > fallbackCap {
		t.Fatalf("fallback replay records not bounded: %d > %d", replays, fallbackCap)
	}

	// The fallback index should list only sessions still referenced by aliases.
	indexKey := claudeThinkingReplayFallbackIndexKVKey(modelFamily)
	index, _ := decodeClaudeThinkingReplayFallbackIndex(client.values[indexKey])
	for _, s := range index.Sessions {
		if len(s.Aliases) == 0 {
			t.Fatalf("fallback index contains unreferenced session %q", s.SessionKey)
		}
	}
}

func TestClaudeThinkingReplayAliasHomeMultiSessionResolve(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg1", "firstA")
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg2", "firstA")
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionB", "msg1", "firstB")

	msgs := []ClaudeThinkingReplayAliasMessage{{Hash: "msg1", Weight: 1}, {Hash: "msg2", Weight: 2}}
	if s, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, "firstA"); !ok || s != "sessionA" {
		t.Fatalf("home resolve: got %q, want sessionA", s)
	}
}

// raceyClaudeThinkingReplayKVClient is a test client that fails the first
// KVCompareAndSwap on a specific alias key and injects a new value, simulating
// a concurrent writer. This verifies that the alias update retries and merges
// instead of overwriting the injected session.
type raceyClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	aliasKey string
	injected []byte
	attempts int
	mu       sync.Mutex
}

func (c *raceyClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	if key == c.aliasKey {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.attempts == 0 {
			c.attempts++
			c.values[key] = append([]byte(nil), c.injected...)
			return false, nil
		}
		c.values[key] = append([]byte(nil), newValue...)
		return true, nil
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayAliasHomeAtomicListUpdates(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, "msg")

	aValue, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
		Sessions: []claudeThinkingReplayAliasHomeSession{
			{SessionKey: "sessionA", FirstUserHash: "firstA", Timestamp: time.Now()},
		},
	})
	abValue, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
		Sessions: []claudeThinkingReplayAliasHomeSession{
			{SessionKey: "sessionA", FirstUserHash: "firstA", Timestamp: time.Now()},
			{SessionKey: "sessionB", FirstUserHash: "firstB", Timestamp: time.Now()},
		},
	})

	base := newFakeClaudeThinkingReplayKVClient()
	base.values[aliasKey] = aValue
	client := &raceyClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		aliasKey:                         aliasKey,
		injected:                         abValue,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	// Register session C. The first CAS sees the initial value A; the racey
	// client simulates a concurrent writer changing it to A+B and returns
	// false. The function must retry, read A+B, append C, and CAS successfully.
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionC", "msg", "firstC")

	raw, ok := client.values[aliasKey]
	if !ok {
		t.Fatal("alias value not found")
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal alias value: %v", err)
	}
	got := make(map[string]string)
	for _, s := range value.Sessions {
		got[s.SessionKey] = s.FirstUserHash
	}
	want := map[string]string{
		"sessionA": "firstA",
		"sessionB": "firstB",
		"sessionC": "firstC",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alias sessions = %v, want %v", got, want)
	}
}

func TestResolveClaudeThinkingReplayAliasRejectsTies(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg", "firstA")
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionB", "msg", "firstB")

	// Without a first-user bonus the two sessions are tied; refuse the match.
	msgs := []ClaudeThinkingReplayAliasMessage{{Hash: "msg", Weight: 1}}
	if _, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, ""); ok {
		t.Fatal("expected no resolve for ambiguous tie")
	}

	// With a matching first-user hash one session uniquely wins.
	if s, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, "firstA"); !ok || s != "sessionA" {
		t.Fatalf("resolve with first-user bonus: got %q ok=%v, want sessionA", s, ok)
	}
}

func TestResolveClaudeThinkingReplayAliasHomeRejectsTies(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionA", "msg", "firstA")
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "sessionB", "msg", "firstB")

	msgs := []ClaudeThinkingReplayAliasMessage{{Hash: "msg", Weight: 1}}
	if _, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, ""); ok {
		t.Fatal("expected no resolve for home ambiguous tie")
	}

	if s, ok := ResolveClaudeThinkingReplaySessionKey(ctx, modelFamily, msgs, "firstA"); !ok || s != "sessionA" {
		t.Fatalf("home resolve with first-user bonus: got %q ok=%v, want sessionA", s, ok)
	}
}

func TestClaudeThinkingReplayAliasHomeCappedAcrossModelsPerCredential(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const credentialHash = "deadbeef"
	modelA := "claude:" + credentialHash + ":modelA"
	modelB := "claude:" + credentialHash + ":modelB"

	// Both model families should map to the same credential-scoped index.
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelA)
	if indexKey != claudeThinkingReplayAliasIndexKVKey(modelB) {
		t.Fatalf("index key not shared across models for same credential: %q vs %q", indexKey, claudeThinkingReplayAliasIndexKVKey(modelB))
	}

	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max+10; i++ {
		mf := modelA
		if i%2 == 1 {
			mf = modelB
		}
		RegisterClaudeThinkingReplayAlias(ctx, mf, "session", messageHashFor(i), "first")
	}

	index, _ := decodeClaudeThinkingReplayAliasIndex(client.values[indexKey])
	if len(index.Aliases) > max {
		t.Fatalf("credential alias cap exceeded across models: %d > %d", len(index.Aliases), max)
	}

	live := 0
	for k, v := range client.values {
		if k != indexKey && aliasValueIsLive(v) {
			live++
		}
	}
	if live > max+1 {
		t.Fatalf("too many live alias keys across models: %d", live)
	}
}

// failingIndexClaudeThinkingReplayKVClient fails every KVCompareAndSwap on the
// index key, simulating a stale read that makes the index CAS lose. It verifies
// that evicted alias values are NOT deleted before a successful index CAS.
type failingIndexClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	indexKey string
	mu       sync.Mutex
}

func (c *failingIndexClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.indexKey {
		return false, nil
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayAliasHomeEvictionIsAtomicWithIndexCAS(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &failingIndexClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	// Pre-populate the index to the cap and create the matching alias values.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	var index claudeThinkingReplayAliasIndex
	now := time.Now()
	for i := 0; i < max; i++ {
		aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(i))
		index.Aliases = append(index.Aliases, claudeThinkingReplayAliasIndexRecord{
			AliasKey:  aliasKey,
			Timestamp: now.Add(-time.Duration(max-i) * time.Second),
		})
		value, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
			Sessions: []claudeThinkingReplayAliasHomeSession{
				{SessionKey: "session", FirstUserHash: "first", Timestamp: now},
			},
		})
		client.values[aliasKey] = value
	}
	indexBytes, _ := json.Marshal(index)
	client.values[indexKey] = indexBytes

	oldestAliasKey := index.Aliases[0].AliasKey

	// The next registration will try to evict the oldest, but the index CAS is
	// forced to fail. The evicted alias value must NOT be deleted.
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHashFor(max), "first")

	if _, ok := client.values[oldestAliasKey]; !ok {
		t.Fatalf("oldest alias %q deleted before successful index CAS", oldestAliasKey)
	}
}

func TestClaudeThinkingReplayAliasHomeRollbackOnIndexFailure(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &failingIndexClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHash, "first")

	raw, ok := client.values[aliasKey]
	if !ok {
		t.Fatalf("alias %q was removed; expected a tombstone", aliasKey)
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("tombstone unmarshal: %v", err)
	}
	if len(value.Sessions) != 0 {
		t.Fatalf("alias %q still has %d sessions after rollback", aliasKey, len(value.Sessions))
	}
}

// readdEvictedClaudeThinkingReplayKVClient simulates a concurrent worker that
// re-registers the evicted alias between the successful index CAS and the
// eviction re-read. The re-read should see the alias back in the index and skip
// deletion.
type readdEvictedClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	indexKey string
	evicted  string
	swapped  bool
}

func (c *readdEvictedClaudeThinkingReplayKVClient) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	if key == c.indexKey && c.swapped {
		idx, ok := decodeClaudeThinkingReplayAliasIndex(c.values[c.indexKey])
		if !ok {
			idx = claudeThinkingReplayAliasIndex{}
		}
		idx.Aliases = append(idx.Aliases, claudeThinkingReplayAliasIndexRecord{
			AliasKey:  c.evicted,
			Timestamp: time.Now(),
		})
		raw, _ := json.Marshal(idx)
		return raw, true, nil
	}
	return c.fakeClaudeThinkingReplayKVClient.KVGet(ctx, key)
}

func (c *readdEvictedClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	swapped, err := c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
	if err == nil && swapped && key == c.indexKey {
		c.swapped = true
	}
	return swapped, err
}

func TestClaudeThinkingReplayAliasHomeEvictionSkipsReaddedAlias(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &readdEvictedClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	now := time.Now()
	var index claudeThinkingReplayAliasIndex
	for i := 0; i < max; i++ {
		aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(i))
		index.Aliases = append(index.Aliases, claudeThinkingReplayAliasIndexRecord{
			AliasKey:  aliasKey,
			Timestamp: now.Add(-time.Duration(max-i) * time.Second),
		})
		value, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
			Sessions: []claudeThinkingReplayAliasHomeSession{
				{SessionKey: "session", FirstUserHash: "first", Timestamp: now},
			},
		})
		client.values[aliasKey] = value
	}
	client.evicted = index.Aliases[0].AliasKey
	indexBytes, _ := json.Marshal(index)
	client.values[indexKey] = indexBytes

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHashFor(max), "first")

	if _, ok := client.values[client.evicted]; !ok {
		t.Fatalf("evicted alias %q was deleted while re-added to index", client.evicted)
	}
}

func TestClaudeThinkingReplayAliasHomeRechecksEvictedAliasValue(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	now := time.Now()
	var index claudeThinkingReplayAliasIndex
	for i := 0; i < max; i++ {
		aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(i))
		index.Aliases = append(index.Aliases, claudeThinkingReplayAliasIndexRecord{
			AliasKey:  aliasKey,
			Timestamp: now.Add(-time.Duration(max-i) * time.Second),
		})
		value, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
			Sessions: []claudeThinkingReplayAliasHomeSession{
				{SessionKey: "session", FirstUserHash: "first", Timestamp: now},
			},
		})
		client.values[aliasKey] = value
	}
	evictedAlias := index.Aliases[0].AliasKey
	indexBytes, _ := json.Marshal(index)
	client.values[indexKey] = indexBytes

	// Simulate a concurrent worker refreshing the evicted alias value after the
	// index record was established but before the eviction pass.
	refreshed, _ := json.Marshal(claudeThinkingReplayAliasHomeValue{
		Sessions: []claudeThinkingReplayAliasHomeSession{
			{SessionKey: "session", FirstUserHash: "first", Timestamp: now.Add(time.Minute)},
		},
	})
	client.values[evictedAlias] = refreshed

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHashFor(max), "first")

	if _, ok := client.values[evictedAlias]; !ok {
		t.Fatalf("evicted alias %q was deleted despite a repopulated value", evictedAlias)
	}
}

// indexGetFailingClaudeThinkingReplayKVClient fails KVGet for the index key.
// This verifies rollback does not depend on an index read succeeding.
type indexGetFailingClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	indexKey string
}

func (c *indexGetFailingClaudeThinkingReplayKVClient) KVGet(ctx context.Context, key string) ([]byte, bool, error) {
	if key == c.indexKey {
		return nil, false, fmt.Errorf("simulated index read failure")
	}
	return c.fakeClaudeThinkingReplayKVClient.KVGet(ctx, key)
}

func TestClaudeThinkingReplayAliasHomeRollbackConditionalOnCommittedValue(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &indexGetFailingClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHash, "first")

	raw, ok := client.values[aliasKey]
	if !ok {
		t.Fatalf("alias %q was removed; expected a tombstone", aliasKey)
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("tombstone unmarshal: %v", err)
	}
	if len(value.Sessions) != 0 {
		t.Fatalf("alias %q still has %d sessions after rollback", aliasKey, len(value.Sessions))
	}
}

// erroredAliasCASClaudeThinkingReplayKVClient simulates an alias CAS that
// returns an error after the value was already applied, leaving a partial
// registration that must be rolled back.
type erroredAliasCASClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	aliasKey string
	errored  bool
}

func (c *erroredAliasCASClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	if key == c.aliasKey && !c.errored {
		c.errored = true
		c.values[key] = append([]byte(nil), newValue...)
		return false, fmt.Errorf("simulated alias CAS error")
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayAliasHomeRollBackOnFailedRegistration(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &erroredAliasCASClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		aliasKey:                         aliasKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHash, "first")

	// Rollback writes an empty tombstone so the alias resolves to nothing
	// rather than a stale unindexed orphan.
	raw, ok := client.values[aliasKey]
	if !ok {
		t.Fatalf("alias %q was removed; expected a tombstone", aliasKey)
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("tombstone unmarshal: %v", err)
	}
	if len(value.Sessions) != 0 {
		t.Fatalf("alias %q still has %d sessions after rollback", aliasKey, len(value.Sessions))
	}
}

type concurrentAliasClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	aliasKey string
	injected []byte
}

func (c *concurrentAliasClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	if key == c.aliasKey {
		c.values[key] = append([]byte(nil), c.injected...)
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayAliasHomeRollBackDoesNotDeleteRepopulatedAlias(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	committed := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "session", FirstUserHash: "first", Timestamp: time.Now()}}}
	committedRaw, _ := json.Marshal(committed)
	injected := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "other", FirstUserHash: "first", Timestamp: time.Now()}}}
	injectedRaw, _ := json.Marshal(injected)

	base := newFakeClaudeThinkingReplayKVClient()
	base.values[aliasKey] = append([]byte(nil), committedRaw...)
	client := &concurrentAliasClaudeThinkingReplayKVClient{fakeClaudeThinkingReplayKVClient: base, aliasKey: aliasKey, injected: injectedRaw}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	rollBackClaudeThinkingReplayAliasHome(ctx, client, aliasKey, indexKey, committedRaw, nil, time.Now())

	if string(client.values[aliasKey]) != string(injectedRaw) {
		t.Fatalf("repopulated alias was overwritten during rollback")
	}
}

func TestClaudeThinkingReplayAliasHomeRollbackRejectsStaleIndexRecord(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	now := time.Now()
	committed := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "session", FirstUserHash: "first", Timestamp: now}}}
	committedRaw, _ := json.Marshal(committed)

	client := newFakeClaudeThinkingReplayKVClient()
	client.values[aliasKey] = append([]byte(nil), committedRaw...)
	// Index references the alias with an older timestamp, so the value is not
	// durably indexed for this registration and should be rolled back.
	index, _ := json.Marshal(claudeThinkingReplayAliasIndex{Aliases: []claudeThinkingReplayAliasIndexRecord{{
		AliasKey:  aliasKey,
		Timestamp: now.Add(-time.Minute),
	}}})
	client.values[indexKey] = index
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	rollBackClaudeThinkingReplayAliasHome(ctx, client, aliasKey, indexKey, committedRaw, nil, now)

	if aliasValueIsLive(client.values[aliasKey]) {
		t.Fatalf("stale index record left alias value live; expected rollback")
	}
}

func TestClaudeThinkingReplayAliasHomeRollbackKeepsFreshIndexRecord(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	now := time.Now()
	committed := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "session", FirstUserHash: "first", Timestamp: now}}}
	committedRaw, _ := json.Marshal(committed)

	client := newFakeClaudeThinkingReplayKVClient()
	client.values[aliasKey] = append([]byte(nil), committedRaw...)
	// Index references the alias with a timestamp from this registration.
	index, _ := json.Marshal(claudeThinkingReplayAliasIndex{Aliases: []claudeThinkingReplayAliasIndexRecord{{
		AliasKey:  aliasKey,
		Timestamp: now,
	}}})
	client.values[indexKey] = index
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	rollBackClaudeThinkingReplayAliasHome(ctx, client, aliasKey, indexKey, committedRaw, nil, now)

	if !aliasValueIsLive(client.values[aliasKey]) {
		t.Fatalf("fresh index record allowed value to be rolled back")
	}
}

func TestClaudeThinkingReplayAliasHomeRollBackRestoresPreviousValue(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	now := time.Now()
	previous := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "old", FirstUserHash: "first", Timestamp: now}}}
	previousRaw, _ := json.Marshal(previous)
	committed := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{
		{SessionKey: "old", FirstUserHash: "first", Timestamp: now},
		{SessionKey: "new", FirstUserHash: "first", Timestamp: now},
	}}
	committedRaw, _ := json.Marshal(committed)

	client := newFakeClaudeThinkingReplayKVClient()
	client.values[aliasKey] = append([]byte(nil), committedRaw...)
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	rollBackClaudeThinkingReplayAliasHome(ctx, client, aliasKey, indexKey, committedRaw, previousRaw, now)

	if string(client.values[aliasKey]) != string(previousRaw) {
		t.Fatalf("rollback did not restore previous alias value: got %s, want %s", client.values[aliasKey], previousRaw)
	}
}

func TestClaudeThinkingReplayAliasHomeRollBackPreservesPriorDespiteConcurrentRepopulation(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	const modelFamily = "claude:test"
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)

	now := time.Now()
	previous := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "old", FirstUserHash: "first", Timestamp: now}}}
	previousRaw, _ := json.Marshal(previous)
	committed := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{
		{SessionKey: "old", FirstUserHash: "first", Timestamp: now},
		{SessionKey: "new", FirstUserHash: "first", Timestamp: now},
	}}
	committedRaw, _ := json.Marshal(committed)
	injected := claudeThinkingReplayAliasHomeValue{Sessions: []claudeThinkingReplayAliasHomeSession{{SessionKey: "other", FirstUserHash: "first", Timestamp: now}}}
	injectedRaw, _ := json.Marshal(injected)

	base := newFakeClaudeThinkingReplayKVClient()
	base.values[aliasKey] = append([]byte(nil), committedRaw...)
	client := &concurrentAliasClaudeThinkingReplayKVClient{fakeClaudeThinkingReplayKVClient: base, aliasKey: aliasKey, injected: injectedRaw}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	rollBackClaudeThinkingReplayAliasHome(ctx, client, aliasKey, indexKey, committedRaw, previousRaw, now)

	if string(client.values[aliasKey]) != string(injectedRaw) {
		t.Fatalf("concurrently repopulated alias was overwritten during rollback: got %s, want %s", client.values[aliasKey], injectedRaw)
	}
}

func TestClaudeThinkingReplayAliasEnforcesByteLimitAndLRU(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	ctx := context.Background()

	// The byte limit is large; construct an alias with a very long modelFamily
	// to push the aggregate size over the cap.
	large := make([]byte, ClaudeThinkingReplayCacheMaxAliasBytes)
	for i := range large {
		large[i] = 'x'
	}
	modelFamily := "claude:" + string(large) + ":model"

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session-a", "msg", "first")
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session-b", "msg2", "first")

	if claudeThinkingReplayAliasBytes > ClaudeThinkingReplayCacheMaxAliasBytes {
		t.Fatalf("alias bytes %d still over the %d cap after enforcement", claudeThinkingReplayAliasBytes, ClaudeThinkingReplayCacheMaxAliasBytes)
	}
}

func TestClaudeThinkingReplayAliasPerKeyEvictsOldestByTimestamp(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	ctx := context.Background()

	modelFamily := "claude:cred:model"
	messageHash := "shared-msg"

	// Fill the per-key list with 8 sessions, each with a distinct timestamp.
	for i := 0; i < ClaudeThinkingReplayCacheMaxAliasesPerKey; i++ {
		useFakeClaudeThinkingReplayKVClient(t, newFakeClaudeThinkingReplayKVClient(), false)
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, fmt.Sprintf("session-%d", i), messageHash, "first")
	}

	// Refresh the oldest one (session-0) so it becomes newest.
	useFakeClaudeThinkingReplayKVClient(t, newFakeClaudeThinkingReplayKVClient(), false)
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session-0", messageHash, "first")

	// Add one more. The oldest remaining by timestamp should be session-1.
	useFakeClaudeThinkingReplayKVClient(t, newFakeClaudeThinkingReplayKVClient(), false)
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session-9", messageHash, "first")

	key := claudeThinkingReplayAliasKey(modelFamily, messageHash)
	list := claudeThinkingReplayAliases[key]
	for _, e := range list {
		if e.sessionKey == "session-1" {
			t.Fatalf("session-1 should have been evicted as oldest after session-0 refresh")
		}
	}
	if len(list) != ClaudeThinkingReplayCacheMaxAliasesPerKey {
		t.Fatalf("per-key list len = %d, want %d", len(list), ClaudeThinkingReplayCacheMaxAliasesPerKey)
	}
}

func TestGetClaudeThinkingReplayWithSnapshotIfExistsDoesNotReserve(t *testing.T) {
	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"
	const sessionKey = "no-nonce-fallback"

	_, _, found, err := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelFamily, sessionKey)
	if err != nil {
		t.Fatalf("GetIfExists error: %v", err)
	}
	if found {
		t.Fatal("expected no existing replay state")
	}
	if client.sets != 0 {
		t.Fatalf("GetIfExists reserved a tombstone: sets=%d", client.sets)
	}

	// A subsequent cache write should then be able to set the value.
	content := []byte(`[{"type":"thinking","thinking":"reason","signature":"EgI="}]`)
	if !CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, sessionKey, content) {
		t.Fatal("CacheClaudeThinkingReplayBestEffort failed")
	}

	contents, _, found, err := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelFamily, sessionKey)
	if err != nil {
		t.Fatalf("GetIfExists after cache error: %v", err)
	}
	if !found || len(contents) != 1 {
		t.Fatalf("expected cached content, found=%v len=%d", found, len(contents))
	}
}

func TestReplaceClaudeThinkingReplayIfUnchangedCASAvoidsOverwrite(t *testing.T) {
	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"
	const sessionKey = "concurrent-fallback"

	_, snapshot, _, err := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelFamily, sessionKey)
	if err != nil {
		t.Fatalf("GetIfExists error: %v", err)
	}
	if !snapshot.loaded || snapshot.found {
		t.Fatalf("expected loaded not-found snapshot, got loaded=%v found=%v", snapshot.loaded, snapshot.found)
	}

	// Another request wins the race and writes first.
	other := []byte(`[{"type":"thinking","thinking":"other","signature":"EgI="}]`)
	if !CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, sessionKey, other) {
		t.Fatal("concurrent cache write failed")
	}

	// The original replace must fail and must not overwrite the winner.
	content := []byte(`[{"type":"thinking","thinking":"loser","signature":"EgI="}]`)
	ok, err := ReplaceClaudeThinkingReplayIfUnchanged(ctx, modelFamily, sessionKey, snapshot, content)
	if err != nil {
		t.Fatalf("Replace error: %v", err)
	}
	if ok {
		t.Fatal("Replace should lose when another writer created the value")
	}

	got, _, found, err := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelFamily, sessionKey)
	if err != nil || !found {
		t.Fatalf("expected winner value, found=%v err=%v", found, err)
	}
	if string(got[0]) != string(other) {
		t.Fatalf("winner value was overwritten: got %q, want %q", got[0], other)
	}
}

func TestClaudeThinkingReplayAliasBatchEvictCarriesIdentity(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()
	ctx := context.Background()
	useFakeClaudeThinkingReplayKVClient(t, newFakeClaudeThinkingReplayKVClient(), false)

	// Each alias key is ~0.5MiB; 130 aliases exceed the 64MiB cap and force a
	// 128-entry eviction batch, leaving the two newest entries.
	const keySize = 1 << 19
	modelFamily := "claude:" + strings.Repeat("x", keySize) + ":model"

	for i := 0; i < 130; i++ {
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, fmt.Sprintf("session-%d", i), messageHashFor(i), "first")
	}

	if claudeThinkingReplayAliasCount != 2 {
		t.Fatalf("alias count = %d, want 2", claudeThinkingReplayAliasCount)
	}

	// The 128-entry batch should have removed the oldest sessions.
	for _, evicted := range []string{"session-0", "session-1"} {
		key := claudeThinkingReplayAliasKey(modelFamily, messageHashFor(0))
		if strings.HasPrefix(evicted, "session-1") {
			key = claudeThinkingReplayAliasKey(modelFamily, messageHashFor(1))
		}
		for _, entry := range claudeThinkingReplayAliases[key] {
			if entry.sessionKey == evicted {
				t.Fatalf("evicted session %q still present", evicted)
			}
		}
	}
	for _, kept := range []string{"session-128", "session-129"} {
		i := 128
		if kept == "session-129" {
			i = 129
		}
		key := claudeThinkingReplayAliasKey(modelFamily, messageHashFor(i))
		found := false
		for _, entry := range claudeThinkingReplayAliases[key] {
			if entry.sessionKey == kept {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("kept session %q not found", kept)
		}
	}
}

func messageHashFor(i int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz"
	s := make([]byte, 0, 8)
	v := i
	for j := 0; j < 8; j++ {
		s = append(s, chars[v%26])
		v /= 26
	}
	return string(s)
}

type raceyTombstoneClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	evictAliasKey string
	liveValue     []byte
	failed        bool
	mu            sync.Mutex
}

func (c *raceyTombstoneClaudeThinkingReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.evictAliasKey && !c.failed {
		var v claudeThinkingReplayAliasHomeValue
		if json.Unmarshal(newValue, &v) == nil && len(v.Sessions) == 0 {
			c.values[key] = append([]byte(nil), c.liveValue...)
			c.sets++
			c.failed = true
			return false, nil
		}
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(context.Background(), key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayAliasHomeEvictionDoesNotDeleteLiveFallback(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"

	evictAliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(0))
	liveValue, err := json.Marshal(claudeThinkingReplayAliasHomeValue{
		Sessions: []claudeThinkingReplayAliasHomeSession{
			{SessionKey: "fb:session-0", FirstUserHash: "first0", Timestamp: time.Now().Add(time.Second)},
		},
	})
	if err != nil {
		t.Fatalf("live value marshal failed: %v", err)
	}

	fake := newFakeClaudeThinkingReplayKVClient()
	client := &raceyTombstoneClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: fake,
		evictAliasKey:                    evictAliasKey,
		liveValue:                        liveValue,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	// Seed the replay record and the first alias.
	CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, "fb:session-0", []byte(`[{"type":"text","text":"x"}]`))
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "fb:session-0", messageHashFor(0), "first0")

	// Fill the per-credential alias cap so the next registration evicts the first alias.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 1; i < max; i++ {
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, fmt.Sprintf("session-%d", i), messageHashFor(i), "first")
	}

	// Trigger eviction. The racey client fails the tombstone CAS and injects
	// a live re-registered alias value for the evicted alias.
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session-new", messageHashFor(max), "first")

	// The evicted alias must still be live.
	raw, found := client.values[evictAliasKey]
	if !found || !claudeThinkingReplayAliasValueRepopulated(raw, time.Time{}) {
		t.Fatalf("evicted alias was deleted instead of preserved")
	}
	var value claudeThinkingReplayAliasHomeValue
	if err := json.Unmarshal(raw, &value); err != nil || len(value.Sessions) == 0 || value.Sessions[0].SessionKey != "fb:session-0" {
		t.Fatalf("evicted alias has unexpected content: %s", raw)
	}

	// The fallback replay record must not have been deleted.
	replayKey := claudeThinkingReplayKVKey(modelFamily, "fb:session-0")
	if _, ok := client.values[replayKey]; !ok {
		t.Fatalf("live replay record %q was deleted", replayKey)
	}

	// The fallback index must still record the alias reference.
	indexKey := claudeThinkingReplayFallbackIndexKVKey(modelFamily)
	raw, found = client.values[indexKey]
	if !found {
		t.Fatalf("fallback index missing")
	}
	var index claudeThinkingReplayFallbackIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("fallback index unmarshal failed: %v", err)
	}
	foundRef := false
	for _, s := range index.Sessions {
		if s.SessionKey != "fb:session-0" {
			continue
		}
		for _, a := range s.Aliases {
			if a == evictAliasKey {
				foundRef = true
				break
			}
		}
	}
	if !foundRef {
		t.Fatalf("fallback index lost live alias reference")
	}
}

func TestClaudeThinkingReplayAliasHomeCrossModelEvictionDeletesCorrectRecord(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelA = "claude:cred:modelA"
	const modelB = "claude:cred:modelB"
	const sessionA = "fb:session-A"

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	// Seed a replay record and alias under model A.
	CacheClaudeThinkingReplayBestEffort(ctx, modelA, sessionA, []byte(`[{"type":"text","text":"x"}]`))
	RegisterClaudeThinkingReplayAlias(ctx, modelA, sessionA, "msgA", "first")

	// Fill the per-credential alias cap with model B aliases. The credential
	// hash is "cred" for both, so the index is shared.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max-1; i++ {
		RegisterClaudeThinkingReplayAlias(ctx, modelB, fmt.Sprintf("sessionB-%d", i), messageHashFor(i), "first")
	}

	// Evict the model A alias from the shared per-credential index. Seed the
	// model B replay record first so it can be verified after eviction.
	CacheClaudeThinkingReplayBestEffort(ctx, modelB, "sessionB-new", []byte(`[{"type":"text","text":"y"}]`))
	RegisterClaudeThinkingReplayAlias(ctx, modelB, "sessionB-new", messageHashFor(max-1), "first")

	// The model A alias value should be tombstoned.
	aliasAKey := claudeThinkingReplayAliasKVKey(modelA, "msgA")
	raw, found := client.values[aliasAKey]
	if !found || claudeThinkingReplayAliasValueRepopulated(raw, time.Time{}) {
		t.Fatalf("model A alias was not evicted")
	}

	// The model A replay record must be tombstoned using model A, not model B.
	replayAKey := claudeThinkingReplayKVKey(modelA, sessionA)
	raw, ok := client.values[replayAKey]
	if !ok {
		t.Fatalf("model A replay record %q missing; expected a tombstone", replayAKey)
	}
	_, _, deleted, okDecode := decodeClaudeThinkingReplayHomeValue(raw)
	if !okDecode || !deleted {
		t.Fatalf("model A replay record %q was not tombstoned", replayAKey)
	}
	if _, _, found, _ := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelA, sessionA); found {
		t.Fatalf("model A replay record %q is still replayable", replayAKey)
	}

	// Model B records should still exist.
	replayBKey := claudeThinkingReplayKVKey(modelB, "sessionB-new")
	if _, ok := client.values[replayBKey]; !ok {
		t.Fatalf("model B replay record %q missing", replayBKey)
	}

	// The fallback index should not reference the deleted model A session.
	indexKey := claudeThinkingReplayFallbackIndexKVKey(modelA)
	raw, found = client.values[indexKey]
	if !found {
		t.Fatalf("fallback index missing")
	}
	var index claudeThinkingReplayFallbackIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("fallback index unmarshal failed: %v", err)
	}
	for _, s := range index.Sessions {
		if s.SessionKey == sessionA {
			t.Fatalf("fallback index still references deleted model A session")
		}
	}
}

type fallbackExhaustClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	indexKey string
	failures int
	mu       sync.Mutex
}

func (c *fallbackExhaustClaudeThinkingReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key == c.indexKey {
		c.failures++
		return false, nil
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(context.Background(), key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayFallbackIndexDoesNotOverwriteOnExhaustion(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayFallbackIndexKVKey(modelFamily)
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(0))

	fake := newFakeClaudeThinkingReplayKVClient()
	client := &fallbackExhaustClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: fake,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	// Seed a stale fallback index that must survive CAS exhaustion without
	// being overwritten by a stale value.
	seed := claudeThinkingReplayFallbackIndex{
		Sessions: []claudeThinkingReplayFallbackSession{{
			SessionKey:  "fb:stale",
			Aliases:     []string{claudeThinkingReplayAliasKVKey(modelFamily, "other")},
			ModelFamily: modelFamily,
			Timestamp:   time.Now(),
		}},
	}
	seedRaw, _ := json.Marshal(seed)
	client.values[indexKey] = seedRaw

	CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, "fb:session", []byte(`[{"type":"text","text":"x"}]`))
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "fb:session", messageHashFor(0), "first")

	raw, ok := client.values[indexKey]
	if !ok || !bytes.Equal(raw, seedRaw) {
		t.Fatalf("fallback index was overwritten after CAS exhaustion")
	}
	if client.failures == 0 {
		t.Fatalf("fallback index CAS was never attempted")
	}
	if _, ok := client.values[aliasKey]; !ok {
		t.Fatalf("alias value not written")
	}
}

type failingFallbackIndexClaudeThinkingReplayKVClient struct {
	*fakeClaudeThinkingReplayKVClient
	indexKey string
}

func (c *failingFallbackIndexClaudeThinkingReplayKVClient) KVCompareAndSwap(ctx context.Context, key string, expected []byte, expectedExists bool, newValue []byte, ttl time.Duration) (bool, error) {
	if key == c.indexKey {
		return false, nil
	}
	return c.fakeClaudeThinkingReplayKVClient.KVCompareAndSwap(ctx, key, expected, expectedExists, newValue, ttl)
}

func TestClaudeThinkingReplayFallbackRecordTombstoneRollsBackOnIndexFailure(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	fallbackIndexKey := claudeThinkingReplayFallbackIndexKVKey(modelFamily)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &failingFallbackIndexClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         fallbackIndexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, "fb:session", []byte(`[{"type":"text","text":"x"}]`))
	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "fb:session", "msg", "first")

	// Fill the per-credential alias cap to evict the first alias.
	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max; i++ {
		session := fmt.Sprintf("fb:session-%d", i)
		CacheClaudeThinkingReplayBestEffort(ctx, modelFamily, session, []byte(`[{"type":"text","text":"x"}]`))
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, session, messageHashFor(i+1), "first")
	}

	// The fallback index CAS always fails; the tombstone must be rolled back.
	if _, _, found, _ := GetClaudeThinkingReplayWithSnapshotIfExists(ctx, modelFamily, "fb:session"); !found {
		t.Fatalf("record was not restored after failed fallback index CAS")
	}
}

func TestClaudeThinkingReplayAliasHomeEvictedTombstoneHasShortTTL(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	client := newFakeClaudeThinkingReplayKVClient()
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	ctx := context.Background()
	const modelFamily = "claude:test"

	max := ClaudeThinkingReplayCacheMaxAliasesPerCredential
	for i := 0; i < max+10; i++ {
		RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHashFor(i), "first")
	}

	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	index, _ := decodeClaudeThinkingReplayAliasIndex(client.values[indexKey])
	if len(index.Aliases) > max {
		t.Fatalf("credential alias cap exceeded: %d > %d", len(index.Aliases), max)
	}

	evictedKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(0))
	if ttl, ok := client.swapsTTLs[evictedKey]; !ok || ttl != claudeThinkingReplayAliasTombstoneTTL {
		t.Fatalf("evicted alias tombstone ttl = %v, want %v", ttl, claudeThinkingReplayAliasTombstoneTTL)
	}

	newKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHashFor(max+9))
	if ttl, ok := client.swapsTTLs[newKey]; !ok || ttl != ClaudeThinkingReplayCacheTTL {
		t.Fatalf("new alias value ttl = %v, want %v", ttl, ClaudeThinkingReplayCacheTTL)
	}
}

func TestClaudeThinkingReplayAliasHomeRollbackTombstoneHasShortTTL(t *testing.T) {
	ClearClaudeThinkingReplayCache()
	defer ClearClaudeThinkingReplayCache()

	ctx := context.Background()
	const modelFamily = "claude:test"
	indexKey := claudeThinkingReplayAliasIndexKVKey(modelFamily)
	messageHash := "new-msg"
	aliasKey := claudeThinkingReplayAliasKVKey(modelFamily, messageHash)

	base := newFakeClaudeThinkingReplayKVClient()
	client := &failingIndexClaudeThinkingReplayKVClient{
		fakeClaudeThinkingReplayKVClient: base,
		indexKey:                         indexKey,
	}
	useFakeClaudeThinkingReplayKVClient(t, client, true)

	RegisterClaudeThinkingReplayAlias(ctx, modelFamily, "session", messageHash, "first")

	raw, ok := client.values[aliasKey]
	if !ok {
		t.Fatalf("alias %q was removed; expected a tombstone", aliasKey)
	}
	if aliasValueIsLive(raw) {
		t.Fatalf("alias %q was committed but not indexed; expected rollback", aliasKey)
	}
	if ttl, ok := client.swapsTTLs[aliasKey]; !ok || ttl != claudeThinkingReplayAliasTombstoneTTL {
		t.Fatalf("rollback tombstone ttl = %v, want %v", ttl, claudeThinkingReplayAliasTombstoneTTL)
	}
}

func TestClaudeThinkingReplayIfExistsLocalAbsenceIsLoaded(t *testing.T) {
	useFakeClaudeThinkingReplayKVClient(t, nil, false)

	const modelFamily = "claude:auth:model"
	const sessionKey = "if-exists-local"
	first := []byte(`[{"type":"thinking","thinking":"first","signature":"sig-1"}]`)
	second := []byte(`[{"type":"thinking","thinking":"second","signature":"sig-2"}]`)

	_, snap1, found, err := GetClaudeThinkingReplayWithSnapshotIfExists(context.Background(), modelFamily, sessionKey)
	if err != nil {
		t.Fatalf("initial IfExists: %v", err)
	}
	if found {
		t.Fatal("initial IfExists should not find replay")
	}
	if !snap1.loaded || snap1.found {
		t.Fatalf("initial IfExists snapshot = loaded %v found %v, want loaded=true found=false", snap1.loaded, snap1.found)
	}

	_, snap2, found, err := GetClaudeThinkingReplayWithSnapshotIfExists(context.Background(), modelFamily, sessionKey)
	if err != nil || found {
		t.Fatalf("second IfExists before replace: found %v, err %v", found, err)
	}
	if !snap2.loaded || snap2.found {
		t.Fatalf("second IfExists snapshot = loaded %v found %v, want loaded=true found=false", snap2.loaded, snap2.found)
	}

	replaced, err := ReplaceClaudeThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snap1, first)
	if err != nil || !replaced {
		t.Fatalf("first replace = %v, err %v", replaced, err)
	}

	replaced, err = ReplaceClaudeThinkingReplayIfUnchanged(context.Background(), modelFamily, sessionKey, snap2, second)
	if err != nil || replaced {
		t.Fatalf("second replace should lose race, got replaced=%v err=%v", replaced, err)
	}

	contents, foundFinal, err := GetClaudeThinkingReplayRequired(context.Background(), modelFamily, sessionKey)
	if err != nil || !foundFinal || len(contents) != 1 {
		t.Fatalf("final contents = %d, err %v; want one turn", len(contents), err)
	}
	if !bytes.Equal(contents[0], first) {
		t.Fatalf("final contents = %s, want %s", contents[0], first)
	}
}
