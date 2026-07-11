package cache

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

type fakeCodexReasoningReplayKVClient struct {
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
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeCodexReasoningReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
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

func TestCodexReasoningReplayRemovalPreservesCompanionToolCalls(t *testing.T) {
	for _, homeMode := range []bool{false, true} {
		name := "local"
		if homeMode {
			name = "home"
		}
		t.Run(name, func(t *testing.T) {
			ClearCodexReasoningReplayCache()
			t.Cleanup(ClearCodexReasoningReplayCache)
			client := newFakeCodexReasoningReplayKVClient()
			useFakeCodexReasoningReplayKVClient(t, client, homeMode, nil)

			model, session := "gpt-5.6-sol", "composite-session"
			items := [][]byte{
				validCodexReasoningReplayItemForTest(2),
				[]byte(`{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}`),
			}
			if homeMode {
				client.values[codexReasoningReplayKVKey(model, session)] = mustCodexReasoningReplayJSON(t, items)
			} else if !CacheCodexReasoningReplayItems(model, session, items) {
				t.Fatal("cache composite replay")
			}

			if err := RemoveCodexReasoningReplayItemsByTypeRequired(context.Background(), model, session, "reasoning"); err != nil {
				t.Fatalf("remove reasoning: %v", err)
			}
			got, found, err := GetCodexReasoningReplayItemsRequired(context.Background(), model, session)
			if err != nil || !found || len(got) != 1 {
				t.Fatalf("filtered replay found=%v items=%q err=%v", found, got, err)
			}
			var item map[string]any
			if err := json.Unmarshal(got[0], &item); err != nil || item["type"] != "function_call" {
				t.Fatalf("filtered replay item=%s err=%v, want function_call", got[0], err)
			}
		})
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
