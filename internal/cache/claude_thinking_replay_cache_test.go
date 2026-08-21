package cache

import (
	"context"
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

func useFakeClaudeThinkingReplayKVClient(t *testing.T, client *fakeClaudeThinkingReplayKVClient, homeMode bool) {
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
