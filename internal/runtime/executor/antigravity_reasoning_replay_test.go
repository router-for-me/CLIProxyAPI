package executor

import (
	"context"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestAntigravityReasoningReplayAccumulatorMultiToolSSEChunks(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	requestPayload := []byte(`{"sessionId":"sess-1","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3-flash-agent", sessionKey: "session:sess-1"}
	acc := newAntigravityReasoningReplayAccumulator(scope, requestPayload)
	if acc == nil {
		t.Fatal("accumulator is nil")
	}
	if acc.contentIndex != 1 || acc.nextPartIndex != 0 {
		t.Fatalf("pending model slot = %d/%d, want 1/0", acc.contentIndex, acc.nextPartIndex)
	}

	line1 := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig-first","functionCall":{"name":"Read","args":{"file_path":"/a"},"id":"id1"}}]}}]}}`)
	line2 := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Read","args":{"file_path":"/b"},"id":"id2"}}]}}]}}`)
	acc.ObserveSSELine(line1)
	acc.ObserveSSELine(line2)
	acc.Flush(context.Background())

	items, ok := internalcache.GetAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-1")
	if !ok || len(items) != 2 {
		t.Fatalf("cached items = %v ok=%v, want 2 items", len(items), ok)
	}
	pi0 := int(gjson.GetBytes(items[0], "partIndex").Int())
	pi1 := int(gjson.GetBytes(items[1], "partIndex").Int())
	if pi0 != 0 || pi1 != 1 {
		t.Fatalf("partIndex = %d,%d, want 0,1", pi0, pi1)
	}
	if got := gjson.GetBytes(items[0], "thoughtSignature").String(); got != "sig-first" {
		t.Fatalf("first sig = %q", got)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayPayloadInjectsCachedToolPart(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"Read","call_id":"id1","args":{"file_path":"/a"},"thoughtSignature":"sig-first"}`)
	if !internalcache.CacheAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-2", [][]byte{item}) {
		t.Fatal("cache write failed")
	}

	req := cliproxyexecutor.Request{}
	opts := cliproxyexecutor.Options{}
	payload := []byte(`{"sessionId":"sess-2","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"Read","response":{"result":"ok"}}}]}]}}`)
	out, scope, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", req, opts, payload)
	if err != nil {
		t.Fatalf("prepare error: %v", err)
	}
	if !scope.valid() {
		t.Fatal("scope invalid")
	}
	if gjson.GetBytes(out, "request.contents.1.role").String() != "model" {
		t.Fatalf("functionCall replay must be model role at [1], got %s", string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "sig-first" {
		t.Fatalf("thoughtSignature = %q, want sig-first", got)
	}
	if !gjson.GetBytes(out, "request.contents.1.parts.0.functionCall").Exists() {
		t.Fatalf("functionCall not injected: %s", string(out))
	}
	if !gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse").Exists() {
		t.Fatalf("functionResponse should follow model functionCall at [2]: %s", string(out))
	}
}

func TestPrepareAntigravityGeminiReasoningReplayInsertsBeforeModelFunctionResponse(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"Read","call_id":"id1","args":{"file_path":"/a"},"thoughtSignature":"sig-first"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-3", [][]byte{item})

	payload := []byte(`{"sessionId":"sess-3","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionResponse":{"id":"id1","name":"Read","response":{"result":"ok"}}}]}]}}`)
	out, _, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !gjson.GetBytes(out, "request.contents.1.parts.0.functionCall").Exists() || gjson.GetBytes(out, "request.contents.1.role").String() != "model" {
		t.Fatalf("want model functionCall at [1]: %s", string(out))
	}
	if !gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse").Exists() {
		t.Fatalf("functionResponse should be at [2]: %s", string(out))
	}
}

func TestMergeAntigravityFunctionCallPartReplayMergesSignatureIntoExistingFunctionCall(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"Read","call_id":"id1","args":{"file_path":"/a"},"thoughtSignature":"sig-first"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-merge", [][]byte{item})

	payload := []byte(`{"sessionId":"sess-merge","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"Read","args":{"file_path":"/a"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"Read","response":{"result":"ok"}}}]}]}}`)
	out, _, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "sig-first" {
		t.Fatalf("thoughtSignature = %q, want sig-first; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayPayloadAppendsStaleThoughtSignatureWithoutNullParts(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"thought_signature","contentIndex":8,"partIndex":3,"thoughtSignature":"stale-thought-sig-ok12"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-stale-text", [][]byte{item})

	payload := []byte(`{"sessionId":"sess-stale-text","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"text":"visible answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if err != nil {
		t.Fatal(err)
	}

	parts := gjson.GetBytes(out, "request.contents.1.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("parts length = %d, want 2; body=%s", len(parts), out)
	}
	for i, part := range parts {
		if part.Type == gjson.Null {
			t.Fatalf("parts.%d is null; body=%s", i, out)
		}
	}
	if got := parts[0].Get("text").String(); got != "visible answer" {
		t.Fatalf("text part = %q, want visible answer; body=%s", got, out)
	}
	if got := parts[1].Get("thoughtSignature").String(); got != "stale-thought-sig-ok12" {
		t.Fatalf("thoughtSignature = %q, want stale-thought-sig-ok12; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayScopeUsesStableSessionWithoutSessionId(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"stable-user-text"}]}]}}`)
	scope := antigravityReasoningReplayScopeFromPayload("gemini-3-flash-agent", payload)
	if !scope.valid() {
		t.Fatal("scope should be valid from stable session hash")
	}
	if !strings.HasPrefix(scope.sessionKey, "session:") {
		t.Fatalf("sessionKey = %q", scope.sessionKey)
	}
}

func TestAntigravityReplayToolCallKeysUsesNativeFunctionCallID(t *testing.T) {
	fc := gjson.Parse(`{"name":"Read","args":{"file_path":"/a"},"id":"id-native"}`)
	keys := antigravityReplayToolCallKeysFromPart(fc)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	fc2 := gjson.Parse(`{"name":"Read","args":{"file_path":"/a"},"id":"id-native-2"}`)
	keys2 := antigravityReplayToolCallKeysFromPart(fc2)
	if keys[0] == keys2[0] {
		t.Fatalf("parallel tool calls should not share replay key: %v vs %v", keys, keys2)
	}
}

func TestAntigravityRequestHasMatchingFunctionResponseWhitespaceCallID(t *testing.T) {
	item := gjson.Parse(`{"call_id":" "}`)
	if !antigravityRequestHasMatchingFunctionResponse(nil, item) {
		t.Fatal("whitespace-only call_id should be treated as empty => true")
	}
}
