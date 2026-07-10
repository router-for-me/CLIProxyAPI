package cache

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/tidwall/gjson"
)

type fakeXAIReasoningReplayKVClient struct {
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

func newFakeXAIReasoningReplayKVClient() *fakeXAIReasoningReplayKVClient {
	return &fakeXAIReasoningReplayKVClient{values: make(map[string][]byte)}
}

func (c *fakeXAIReasoningReplayKVClient) KVGet(_ context.Context, key string) ([]byte, bool, error) {
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

func (c *fakeXAIReasoningReplayKVClient) KVSet(_ context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error) {
	c.setCount++
	c.lastSetTTL = opts.EX
	if c.setErr != nil {
		return false, c.setErr
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *fakeXAIReasoningReplayKVClient) KVDel(_ context.Context, keys ...string) (int64, error) {
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

func (c *fakeXAIReasoningReplayKVClient) KVExpire(_ context.Context, _ string, ttl time.Duration) (bool, error) {
	c.expireCount++
	c.lastExpireTTL = ttl
	if c.expireErr != nil {
		return false, c.expireErr
	}
	return true, nil
}

func useFakeXAIReasoningReplayKVClient(t *testing.T, client *fakeXAIReasoningReplayKVClient, homeMode bool, errClient error) {
	t.Helper()
	previous := currentXAIReasoningReplayKVClient
	currentXAIReasoningReplayKVClient = func() (xaiReasoningReplayKVClient, bool, error) {
		return client, homeMode, errClient
	}
	t.Cleanup(func() {
		currentXAIReasoningReplayKVClient = previous
	})
}

func mustXAIReasoningReplayJSON(t *testing.T, items [][]byte) []byte {
	t.Helper()
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal replay items: %v", err)
	}
	return raw
}

func TestXAIReasoningReplayCacheRejectsCodexEncryptedContent(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	if CacheXAIReasoningReplayItem("grok-4.3", "claude:xai-cache-test", []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"gAAAAABinvalid-gpt-shape"}`)) {
		t.Fatal("xAI replay cache should reject GPT/Codex-shaped encrypted_content")
	}
	if _, ok := GetXAIReasoningReplayItem("grok-4.3", "claude:xai-cache-test"); ok {
		t.Fatal("xAI replay cache should not store GPT/Codex-shaped encrypted_content")
	}
}

func TestXAIReasoningReplayCacheStoresGrokEncryptedContent(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	encryptedContent := validGrokEncryptedContentForReplayCacheTest()
	if !CacheXAIReasoningReplayItem("grok-4.3", "claude:xai-cache-test", []byte(`{"type":"reasoning","summary":[{"type":"summary_text","text":"visible"}],"content":null,"encrypted_content":"`+encryptedContent+`"}`)) {
		t.Fatal("xAI replay cache should store valid Grok encrypted_content")
	}
	item, ok := GetXAIReasoningReplayItem("grok-4.3", "claude:xai-cache-test")
	if !ok {
		t.Fatal("xAI replay cache item missing after store")
	}
	if got := gjson.GetBytes(item, "encrypted_content").String(); got != encryptedContent {
		t.Fatalf("encrypted_content = %q, want %q; item=%s", got, encryptedContent, string(item))
	}
	if got := gjson.GetBytes(item, "summary").Array(); len(got) != 0 {
		t.Fatalf("summary length = %d, want normalized empty summary; item=%s", len(got), string(item))
	}
}

func TestXAIReasoningReplayCacheStoresAssistantMessageWithReasoning(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)
	encryptedContent := validGrokEncryptedContentForReplayCacheTest()

	items := [][]byte{
		[]byte(`{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"visible"}],"encrypted_content":"` + encryptedContent + `"}`),
		[]byte(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer","annotations":[],"logprobs":[]}]}`),
	}
	if !CacheXAIReasoningReplayItems("grok-4.5", "prompt-cache:session", items) {
		t.Fatal("expected reasoning replay items to be cached")
	}

	got, ok := GetXAIReasoningReplayItems("grok-4.5", "prompt-cache:session")
	if !ok || len(got) != 2 {
		t.Fatalf("cached items = %q, %v, want two items", got, ok)
	}
	if gjson.GetBytes(got[0], "encrypted_content").String() != encryptedContent {
		t.Fatalf("reasoning encrypted_content not preserved: %s", got[0])
	}
	if gotText := gjson.GetBytes(got[1], "content.0.text").String(); gotText != "answer" {
		t.Fatalf("assistant message text = %q, want answer; item=%s", gotText, got[1])
	}
	if gjson.GetBytes(got[1], "id").Exists() || gjson.GetBytes(got[1], "status").Exists() {
		t.Fatalf("assistant message transport fields were not stripped: %s", got[1])
	}
}

func TestXAIReasoningReplayCacheRejectsAssistantMessageWithoutReasoning(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)

	items := [][]byte{
		[]byte(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"answer"}]}`),
	}
	if CacheXAIReasoningReplayItems("grok-4.5", "prompt-cache:message-only", items) {
		t.Fatal("message-only replay batch must not be cached")
	}
	if _, ok := GetXAIReasoningReplayItems("grok-4.5", "prompt-cache:message-only"); ok {
		t.Fatal("message-only replay batch unexpectedly exists in cache")
	}
}

func TestXAIReasoningReplayRequiredHomeExpireFailureReturnsItems(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)
	client := newFakeXAIReasoningReplayKVClient()
	client.expireErr = errors.New("expire failed")
	key := xaiReasoningReplayKVKey("grok-4.3", "session-home")
	item := []byte(`{"type":"reasoning","summary":[],"content":null,"encrypted_content":"` + validGrokEncryptedContentForReplayCacheTest() + `"}`)
	client.values[key] = mustXAIReasoningReplayJSON(t, [][]byte{item})
	useFakeXAIReasoningReplayKVClient(t, client, true, nil)

	items, found, errGet := GetXAIReasoningReplayItemsRequired(context.Background(), "grok-4.3", "session-home")
	if errGet != nil {
		t.Fatalf("GetXAIReasoningReplayItemsRequired() error = %v", errGet)
	}
	if !found || len(items) != 1 || string(items[0]) != string(item) {
		t.Fatalf("GetXAIReasoningReplayItemsRequired() = %q, %v, want item, true", items, found)
	}
	if client.expireCount != 1 || client.lastExpireTTL != XAIReasoningReplayCacheTTL {
		t.Fatalf("KVExpire count/ttl = %d/%v, want 1/%v", client.expireCount, client.lastExpireTTL, XAIReasoningReplayCacheTTL)
	}
}

func validGrokEncryptedContentForReplayCacheTest() string {
	buf := make([]byte, 0, 256)
	for i := 0; len(buf) < 256; i++ {
		sum := sha256.Sum256([]byte{byte(i), byte(i >> 8), byte(i >> 16), 99})
		buf = append(buf, sum[:]...)
	}
	return base64.RawStdEncoding.EncodeToString(buf[:256])
}

func TestXAIReasoningReplayCacheStoresRefusalMessagePart(t *testing.T) {
	ClearXAIReasoningReplayCache()
	t.Cleanup(ClearXAIReasoningReplayCache)
	encryptedContent := validGrokEncryptedContentForReplayCacheTest()

	items := [][]byte{
		[]byte(`{"type":"reasoning","summary":[],"encrypted_content":"` + encryptedContent + `"}`),
		[]byte(`{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I cannot help with that"}]}`),
	}
	if !CacheXAIReasoningReplayItems("grok-4.5", "prompt-cache:refusal", items) {
		t.Fatal("expected refusal message with reasoning to be cached")
	}
	got, ok := GetXAIReasoningReplayItems("grok-4.5", "prompt-cache:refusal")
	if !ok || len(got) != 2 {
		t.Fatalf("cached items = %q, %v, want reasoning + refusal message", got, ok)
	}
	if gjson.GetBytes(got[1], "content.0.type").String() != "refusal" {
		t.Fatalf("message part type = %s, want refusal; item=%s", gjson.GetBytes(got[1], "content.0.type").String(), got[1])
	}
	if gjson.GetBytes(got[1], "content.0.refusal").String() != "I cannot help with that" {
		t.Fatalf("refusal text missing; item=%s", got[1])
	}
	if gjson.GetBytes(got[1], "content.0.text").Exists() {
		t.Fatalf("refusal part should not use text field; item=%s", got[1])
	}
}
