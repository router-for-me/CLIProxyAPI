package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

type fakeClaudeThinkingReplayKVClient struct {
	values  map[string][]byte
	sets    int
	dels    int
	getErr  error
	setErr  error
	delErr  error
	swapErr error
}

func newFakeClaudeThinkingReplayKVClient() *fakeClaudeThinkingReplayKVClient {
	return &fakeClaudeThinkingReplayKVClient{values: make(map[string][]byte)}
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

func (c *fakeClaudeThinkingReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, _ bool, newValue []byte, _ time.Duration) (bool, error) {
	if c.swapErr != nil {
		return false, c.swapErr
	}
	current, ok := c.values[key]
	if !ok && expected == nil {
		c.values[key] = append([]byte(nil), newValue...)
		c.sets++
		return true, nil
	}
	if ok && string(current) == string(expected) {
		c.values[key] = append([]byte(nil), newValue...)
		c.sets++
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

	// The oldest entries should have been deleted.
	live := 0
	for k := range client.values {
		if k != indexKey {
			live++
		}
	}
	if live > max+1 { // +1 for the index key itself
		t.Fatalf("too many live alias keys: %d", live)
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
	for k := range client.values {
		if k != indexKey {
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

	if _, ok := client.values[aliasKey]; ok {
		t.Fatalf("alias %q was committed but not indexed; expected rollback", aliasKey)
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

	if _, ok := client.values[aliasKey]; ok {
		t.Fatalf("alias %q was committed but not indexed; expected rollback conditional on committed value", aliasKey)
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

	if _, ok := client.values[aliasKey]; ok {
		t.Fatalf("alias %q was left after a failed CAS; expected rollback", aliasKey)
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
