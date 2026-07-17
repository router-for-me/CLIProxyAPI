package cache

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/tidwall/gjson"
)

type fakeCodexReasoningReplayKVClient struct {
	mu            sync.Mutex
	values        map[string][]byte
	getErr        error
	setErr        error
	delErr        error
	expireErr     error
	getCount      int
	setCount      int
	delCount      int
	expireCount   int
	lastSetTTL    time.Duration
	lastExpireTTL time.Duration
}

func newFakeCodexReasoningReplayKVClient() *fakeCodexReasoningReplayKVClient {
	return &fakeCodexReasoningReplayKVClient{values: make(map[string][]byte)}
}

func (c *fakeCodexReasoningReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getCount++
	if c.getErr != nil {
		return nil, false, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *fakeCodexReasoningReplayKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeCodexReasoningReplayKVClient) KVCompareAndSwap(_ context.Context, key string, expected []byte, expectedExists bool, value []byte, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCount++
	c.lastSetTTL = ttl
	if c.setErr != nil {
		return false, c.setErr
	}
	current, exists := c.values[key]
	if exists != expectedExists || (exists && !bytes.Equal(current, expected)) {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeCodexReasoningReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delCount++
	if c.delErr != nil {
		return 0, c.delErr
	}
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

func (c *fakeCodexReasoningReplayKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeCodexReasoningReplayKVClient(t *testing.T, client *fakeCodexReasoningReplayKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentCodexReasoningReplayKVClient
	currentCodexReasoningReplayKVClient = func() (codexReasoningReplayKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentCodexReasoningReplayKVClient = previous
	})
}

func validCodexReasoningReplayEncryptedContentForTest(seed byte) string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = seed + byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func validCodexReasoningReplayItemForTest(seed byte) []byte {
	return []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + validCodexReasoningReplayEncryptedContentForTest(seed) + `"}`)
}

func mustCodexReasoningReplayJSON(t *testing.T, items [][]byte) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(items)
	if errMarshal != nil {
		t.Fatalf("marshal replay items: %v", errMarshal)
	}
	return raw
}

func TestCodexReasoningReplayCacheRejectsInvalidItems(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	if CacheCodexReasoningReplayItem("gpt-5.4", "session", []byte(`{"type":"reasoning","encrypted_content":"bad","summary":[]}`)) {
		t.Fatal("invalid encrypted_content should not be cached")
	}
	if _, ok := GetCodexReasoningReplayItem("gpt-5.4", "session"); ok {
		t.Fatal("invalid item was cached")
	}
}

func TestCodexReasoningReplayRequiredHomeReadAndSlidingExpire(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)
	client := newFakeCodexReasoningReplayKVClient()
	key := codexReasoningReplayKVKey("gpt-5.4", "session-home")
	item := validCodexReasoningReplayItemForTest(3)
	client.values[key] = mustCodexReasoningReplayJSON(t, [][]byte{item})
	useFakeCodexReasoningReplayKVClient(t, client, true, nil)

	items, found, errGet := GetCodexReasoningReplayItemsRequired(context.Background(), "gpt-5.4", "session-home")
	if errGet != nil {
		t.Fatalf("GetCodexReasoningReplayItemsRequired() error = %v", errGet)
	}
	if !found || len(items) != 1 || string(items[0]) != string(item) {
		t.Fatalf("GetCodexReasoningReplayItemsRequired() = %q, %v, want item, true", items, found)
	}
	if client.expireCount != 1 || client.lastExpireTTL != CodexReasoningReplayCacheTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, CodexReasoningReplayCacheTTL)
	}
}

func TestCodexReasoningReplayRequiredHomeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client *fakeCodexReasoningReplayKVClient
	}{
		{name: "get", client: &fakeCodexReasoningReplayKVClient{values: make(map[string][]byte), getErr: errors.New("get failed")}},
		{name: "expire", client: &fakeCodexReasoningReplayKVClient{values: map[string][]byte{
			codexReasoningReplayKVKey("gpt-5.4", "session-home"): mustCodexReasoningReplayJSON(t, [][]byte{validCodexReasoningReplayItemForTest(4)}),
		}, expireErr: errors.New("expire failed")}},
		{name: "delete", client: &fakeCodexReasoningReplayKVClient{values: make(map[string][]byte), delErr: errors.New("delete failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useFakeCodexReasoningReplayKVClient(t, tc.client, true, nil)
			switch tc.name {
			case "delete":
				if errDel := DeleteCodexReasoningReplayItemRequired(context.Background(), "gpt-5.4", "session-home"); errDel == nil {
					t.Fatalf("DeleteCodexReasoningReplayItemRequired() error = nil, want error")
				}
			default:
				if _, _, errGet := GetCodexReasoningReplayItemsRequired(context.Background(), "gpt-5.4", "session-home"); errGet == nil {
					t.Fatalf("GetCodexReasoningReplayItemsRequired() error = nil, want error")
				}
			}
		})
	}
}

func TestCodexReasoningReplayBestEffortHomeWriteFailureDoesNotUseLocalCache(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)
	client := newFakeCodexReasoningReplayKVClient()
	client.setErr = errors.New("set failed")
	useFakeCodexReasoningReplayKVClient(t, client, true, nil)

	if CacheCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "session-home", [][]byte{validCodexReasoningReplayItemForTest(5)}) {
		t.Fatalf("CacheCodexReasoningReplayItemsBestEffort() = true, want false")
	}
	useFakeCodexReasoningReplayKVClient(t, newFakeCodexReasoningReplayKVClient(), false, nil)
	if _, found := GetCodexReasoningReplayItems("gpt-5.4", "session-home"); found {
		t.Fatalf("local replay cache was populated after Home best-effort write failure")
	}
}

func TestCodexReasoningReplayAppendPreservesCumulativeTurnsInHome(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)
	client := newFakeCodexReasoningReplayKVClient()
	useFakeCodexReasoningReplayKVClient(t, client, true, nil)

	first := [][]byte{
		[]byte(`{"type":"` + CodexReasoningReplayTurnType + `","id":"turn-1","assistant_fingerprint":"answer-1"}`),
		validCodexReasoningReplayItemForTest(11),
	}
	second := [][]byte{
		[]byte(`{"type":"` + CodexReasoningReplayTurnType + `","id":"turn-2","call_ids":["call-2"]}`),
		validCodexReasoningReplayItemForTest(12),
	}
	if !AppendCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "session-home-append", first) {
		t.Fatal("first append failed")
	}
	if !AppendCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "session-home-append", second) {
		t.Fatal("second append failed")
	}
	if !AppendCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "session-home-append", second) {
		t.Fatal("duplicate append failed")
	}

	items, found, errGet := GetCodexReasoningReplayItemsRequired(context.Background(), "gpt-5.4", "session-home-append")
	if errGet != nil || !found {
		t.Fatalf("get cumulative turns = found %v err %v", found, errGet)
	}
	if len(items) != 4 {
		t.Fatalf("cumulative item count = %d, want 4: %q", len(items), items)
	}
	if got := gjson.GetBytes(items[0], "id").String(); got != "turn-1" {
		t.Fatalf("first turn id = %q, want turn-1", got)
	}
	if got := gjson.GetBytes(items[2], "id").String(); got != "turn-2" {
		t.Fatalf("second turn id = %q, want turn-2", got)
	}
}

func TestCodexReasoningReplayAppendHomeCASPreservesConcurrentTurns(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)
	client := newFakeCodexReasoningReplayKVClient()
	useFakeCodexReasoningReplayKVClient(t, client, true, nil)

	const turnCount = 16
	var waitGroup sync.WaitGroup
	for turn := 0; turn < turnCount; turn++ {
		waitGroup.Add(1)
		go func(turnID int) {
			defer waitGroup.Done()
			items := [][]byte{
				[]byte(fmt.Sprintf(`{"type":"%s","id":"turn-%d"}`, CodexReasoningReplayTurnType, turnID)),
				validCodexReasoningReplayItemForTest(byte(30 + turnID)),
			}
			if !AppendCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "session-home-concurrent", items) {
				t.Errorf("append turn %d failed", turnID)
			}
		}(turn)
	}
	waitGroup.Wait()

	items, found, errGet := GetCodexReasoningReplayItemsRequired(context.Background(), "gpt-5.4", "session-home-concurrent")
	if errGet != nil || !found {
		t.Fatalf("get concurrent turns = found %v err %v", found, errGet)
	}
	if len(items) != turnCount*2 {
		t.Fatalf("concurrent cumulative item count = %d, want %d", len(items), turnCount*2)
	}
}

func TestCodexReasoningReplayAppendBoundsTurnsPerEntry(t *testing.T) {
	items := make([][]byte, 0, (CodexReasoningReplayCacheMaxTurnsPerEntry+1)*2)
	for turn := 0; turn <= CodexReasoningReplayCacheMaxTurnsPerEntry; turn++ {
		items = append(items,
			[]byte(fmt.Sprintf(`{"type":"%s","id":"turn-%d"}`, CodexReasoningReplayTurnType, turn)),
			validCodexReasoningReplayItemForTest(byte(50+turn)),
		)
	}

	trimmed := trimCodexReasoningReplayItems(items)
	if len(trimmed) != CodexReasoningReplayCacheMaxTurnsPerEntry*2 {
		t.Fatalf("trimmed item count = %d, want %d", len(trimmed), CodexReasoningReplayCacheMaxTurnsPerEntry*2)
	}
	if firstID := gjson.GetBytes(trimmed[0], "id").String(); firstID != "turn-1" {
		t.Fatalf("first retained turn = %q, want turn-1", firstID)
	}
}

func TestCodexReasoningReplayHomeRejectsEmptyScopeWithoutKV(t *testing.T) {
	client := newFakeCodexReasoningReplayKVClient()
	useFakeCodexReasoningReplayKVClient(t, client, true, nil)

	if _, found, errGet := GetCodexReasoningReplayItemsRequired(context.Background(), "", "session-home"); errGet != nil || found {
		t.Fatalf("GetCodexReasoningReplayItemsRequired(empty model) = found %v err %v, want false nil", found, errGet)
	}
	if CacheCodexReasoningReplayItemsBestEffort(context.Background(), "gpt-5.4", "", [][]byte{validCodexReasoningReplayItemForTest(6)}) {
		t.Fatalf("CacheCodexReasoningReplayItemsBestEffort(empty session) = true, want false")
	}
	if errDel := DeleteCodexReasoningReplayItemRequired(context.Background(), "gpt-5.4", ""); errDel != nil {
		t.Fatalf("DeleteCodexReasoningReplayItemRequired(empty session) error = %v", errDel)
	}
	if client.getCount != 0 || client.setCount != 0 || client.delCount != 0 || client.expireCount != 0 {
		t.Fatalf("KV calls = get %d set %d del %d expire %d, want all zero", client.getCount, client.setCount, client.delCount, client.expireCount)
	}
}

func TestCodexReasoningReplayCacheScopesByModelAndSession(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningReplayEncryptedContentForTest(7)
	if !CacheCodexReasoningReplayItem("gpt-5.4", "session-a", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}`)) {
		t.Fatal("valid item was not cached")
	}

	if _, ok := GetCodexReasoningReplayItem("gpt-5.5", "session-a"); ok {
		t.Fatal("cache should not hit across models")
	}
	if _, ok := GetCodexReasoningReplayItem("gpt-5.4", "session-b"); ok {
		t.Fatal("cache should not hit across sessions")
	}

	item, ok := GetCodexReasoningReplayItem("gpt-5.4", "session-a")
	if !ok {
		t.Fatal("cache miss for original model and session")
	}
	if string(item) != `{"type":"reasoning","summary":[],"content":null,"encrypted_content":"`+encryptedContent+`"}` {
		t.Fatalf("normalized item = %s", string(item))
	}
}

func TestCodexReasoningReplayCacheBatchEvictsWhenFull(t *testing.T) {
	ClearCodexReasoningReplayCache()
	t.Cleanup(ClearCodexReasoningReplayCache)

	encryptedContent := validCodexReasoningReplayEncryptedContentForTest(9)
	item := []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + encryptedContent + `"}`)
	for i := 0; i <= CodexReasoningReplayCacheMaxEntries; i++ {
		if !CacheCodexReasoningReplayItem("gpt-5.4", fmt.Sprintf("session-%d", i), item) {
			t.Fatalf("cache insert %d failed", i)
		}
	}

	codexReasoningReplayMu.Lock()
	gotLen := len(codexReasoningReplayEntries)
	codexReasoningReplayMu.Unlock()
	if gotLen >= CodexReasoningReplayCacheMaxEntries {
		t.Fatalf("cache entries = %d, want batch eviction below max %d", gotLen, CodexReasoningReplayCacheMaxEntries)
	}
}
