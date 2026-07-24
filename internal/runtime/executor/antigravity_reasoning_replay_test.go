package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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
	line2 := []byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Read","args":{"file_path":"/b"},"id":"id2"}}]},"finishReason":"STOP"}]}}`)
	acc.ObserveSSELine(line1)
	acc.ObserveSSELine(line2)
	acc.Commit(context.Background())

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

func TestPrepareAntigravityGeminiReasoningReplayPayloadRejectsToolOutputsAcrossUserBoundary(t *testing.T) {
	payload := []byte(`{"sessionId":"tool-output-boundary","request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run","args":{}}},{"functionCall":{"id":"call-2","name":"run","args":{}}}]},{"role":"model","parts":[{"functionResponse":{"id":"call-1","name":"run","response":{"result":"one"}}}]},{"role":"user","parts":[{"text":"boundary"}]},{"role":"model","parts":[{"functionResponse":{"id":"call-2","name":"run","response":{"result":"two"}}}]}]}}`)
	_, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare == nil {
		t.Fatal("invalid tool output history was not rejected")
	}
	status, ok := errPrepare.(statusErr)
	if !ok || status.code != http.StatusBadRequest {
		t.Fatalf("prepare error = %#v, want local 400", errPrepare)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayPayloadKeepsCacheForAlreadyInvalidToolHistory(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)
	const model, sessionKey = "gemini-3.6-flash-high", "session:invalid-injected-tool-history"
	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"call_id":"call-2","name":"run","args":{},"thoughtSignature":"injected-tool-signature-123456789"}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("cache write failed")
	}
	payload := []byte(`{"sessionId":"invalid-injected-tool-history","request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run","args":{}}}]},{"role":"model","parts":[{"functionResponse":{"id":"call-2","name":"run","response":{"result":"two"}}}]}]}}`)
	_, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare == nil {
		t.Fatal("invalid replay-injected history was not rejected")
	}
	if _, found := internalcache.GetAntigravityReasoningReplayItems(model, sessionKey); !found {
		t.Fatal("already-invalid client history cleared replay state")
	}
}

func TestPrepareAntigravityGeminiReasoningReplayPayloadKeepsCacheForClientMalformedHistory(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)
	const model, sessionKey = "gemini-3.6-flash-high", "session:client-malformed-history"
	payload := []byte(`{"sessionId":"client-malformed-history","request":{"contents":[{"role":"model","parts":[{"text":"answer"}]},{"role":"model","parts":[{"functionResponse":{"id":"orphan","name":"run","response":{"result":"bad"}}}]}]}}`)
	kind, fingerprint := antigravityReplayPartFingerprint(gjson.Parse(`{"text":"answer"}`))
	item := buildAntigravityThoughtSignatureItem(0, 0, "valid-cache-signature-123456789", kind, fingerprint)
	item = antigravitySetReplayItemContextHash(item, payload, 0)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("cache write failed")
	}
	_, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare == nil {
		t.Fatal("client-malformed history was not rejected")
	}
	if _, found := internalcache.GetAntigravityReasoningReplayItems(model, sessionKey); !found {
		t.Fatal("client-malformed history cleared unrelated valid replay state")
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

func TestPrepareAntigravityGeminiReasoningReplayPayloadSanitizesInsertedUnsignedToolPart(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"Read","call_id":"id1","args":{"file_path":"/a"}}`)
	if !internalcache.CacheAntigravityReasoningReplayItems("gemini-3-flash-agent", "session:sess-unsigned-replay", [][]byte{item}) {
		t.Fatal("cache write failed")
	}

	payload := []byte(`{"sessionId":"sess-unsigned-replay","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"Read","response":{"result":"ok"}}}]}]}}`)
	payload = sanitizeAntigravityGeminiRequestSignatures("gemini-3-flash-agent", payload)
	out, _, err := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if err != nil {
		t.Fatalf("prepare error: %v", err)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "skip_thought_signature_validator" {
		t.Fatalf("inserted first synthetic functionCall signature = %q, want bypass sentinel; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.2.role").String(); got != "model" {
		t.Fatalf("replayed functionResponse role = %q, want native model role; output=%s", got, out)
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

func TestPrepareAntigravityGeminiReasoningReplayPayloadDropsStaleThoughtSignature(t *testing.T) {
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
	if len(parts) != 1 {
		t.Fatalf("parts length = %d, want unchanged single text part; body=%s", len(parts), out)
	}
	if got := parts[0].Get("text").String(); got != "visible answer" {
		t.Fatalf("text part = %q, want visible answer; body=%s", got, out)
	}
	if got := parts[0].Get("thoughtSignature").String(); got != "" {
		t.Fatalf("stale thoughtSignature must not move to another turn, got %q; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayAccumulatesCompleteTextSignatureChain(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:chain-session"
		sig1       = "native-signature-turn-one-123456"
		sig2       = "native-signature-turn-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: model, sessionKey: sessionKey}

	request1 := []byte(`{"sessionId":"chain-session","request":{"contents":[{"role":"user","parts":[{"text":"turn one"}]}]}}`)
	acc1 := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc1.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer-"}]}}]}}`))
	acc1.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"one"}]}}]}}`))
	acc1.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + sig1 + `"}]},"finishReason":"STOP"}]}}`))
	acc1.Commit(context.Background())

	request2 := []byte(`{"sessionId":"chain-session","request":{"contents":[{"role":"user","parts":[{"text":"turn one"}]},{"role":"model","parts":[{"text":"answer-one"}]},{"role":"user","parts":[{"text":"turn two"}]}]}}`)
	prepared2, _, errPrepare2 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare2 != nil {
		t.Fatal(errPrepare2)
	}
	if got := gjson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 2 signature = %q, want %q; body=%s", got, sig1, prepared2)
	}
	if got := gjson.GetBytes(prepared2, "request.contents.1.parts.#").Int(); got != 1 {
		t.Fatalf("turn 2 must attach signature in place, parts=%d; body=%s", got, prepared2)
	}

	acc2 := newAntigravityReasoningReplayAccumulator(scope, prepared2)
	acc2.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer-two"}]}}]}}`))
	acc2.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`))
	acc2.Commit(context.Background())

	items, ok := internalcache.GetAntigravityReasoningReplayItems(model, sessionKey)
	if !ok || len(items) != 2 {
		t.Fatalf("cached chain length = %d ok=%v, want 2", len(items), ok)
	}

	request3 := []byte(`{"sessionId":"chain-session","request":{"contents":[{"role":"user","parts":[{"text":"turn one"}]},{"role":"model","parts":[{"text":"answer-one"}]},{"role":"user","parts":[{"text":"turn two"}]},{"role":"model","parts":[{"text":"answer-two"}]},{"role":"user","parts":[{"text":"turn three"}]}]}}`)
	prepared3, _, errPrepare3 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request3)
	if errPrepare3 != nil {
		t.Fatal(errPrepare3)
	}
	if got := gjson.GetBytes(prepared3, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 3 first signature = %q, want %q; body=%s", got, sig1, prepared3)
	}
	if got := gjson.GetBytes(prepared3, "request.contents.3.parts.0.thoughtSignature").String(); got != sig2 {
		t.Fatalf("turn 3 second signature = %q, want %q; body=%s", got, sig2, prepared3)
	}
	if got := len(gjson.GetBytes(prepared3, "request.contents.1.parts").Array()) + len(gjson.GetBytes(prepared3, "request.contents.3.parts").Array()); got != 2 {
		t.Fatalf("signatures must remain attached to native text parts, total parts=%d; body=%s", got, prepared3)
	}
}

func TestAntigravityReasoningReplaySplitsConsecutiveSignedTextSegments(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "consecutive-text-signature-one-123456"
		sig2 = "consecutive-text-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:consecutive-signed-text"}
	request1 := []byte(`{"sessionId":"consecutive-signed-text","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"b","thoughtSignature":"` + sig1 + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"c","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"consecutive-signed-text","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"ab"},{"text":"c"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first consecutive text signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second consecutive text signature = %q, want %q; body=%s", got, sig2, out)
	}
}

func TestAntigravityReasoningReplaySignatureOnlyCarrierEndsTextSegment(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "trailing-text-signature-one-123456"
		sig2 = "trailing-text-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:trailing-signed-text"}
	request1 := []byte(`{"sessionId":"trailing-signed-text","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"a"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + sig1 + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"b"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"c","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"trailing-signed-text","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"a"},{"text":"bc"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first trailing text signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second trailing text signature = %q, want %q; body=%s", got, sig2, out)
	}
}

func TestAntigravityReasoningReplayDropsUnmatchedConsecutiveCarrier(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "matched-detached-signature-one-123456"
		sig2 = "unmatched-detached-signature-two-123456"
		sig3 = "matched-text-signature-three-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:unmatched-detached"}
	request1 := []byte(`{"sessionId":"unmatched-detached","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"a"},{"text":"","thoughtSignature":"` + sig1 + `"},{"text":"","thoughtSignature":"` + sig2 + `"},{"text":"b","thoughtSignature":"` + sig3 + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"unmatched-detached","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"a"},{"text":"b"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig3 {
		t.Fatalf("second signature = %q, want %q; body=%s", got, sig3, out)
	}
	if strings.Contains(string(out), sig2) {
		t.Fatalf("unmatched carrier must not replace a semantic signature; body=%s", out)
	}
}

func TestAntigravityReasoningReplayDuplicateCarrierDoesNotSplitSegment(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "duplicate-thought-signature-one-123456"
		sig2 = "following-thought-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:duplicate-carrier"}
	request1 := []byte(`{"sessionId":"duplicate-carrier","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"a","thought":true},{"text":"","thought":true,"thoughtSignature":"` + sig1 + `"},{"text":"b","thought":true},{"text":"","thought":true,"thoughtSignature":"` + sig1 + `"},{"text":"c","thought":true,"thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"duplicate-carrier","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"a","thought":true},{"text":"bc","thought":true}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first thought signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second thought signature = %q, want %q; body=%s", got, sig2, out)
	}
}

func TestAntigravityReasoningReplayDirectTextSignatureWinsOverUnboundPrefix(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		prefixSig = "unbound-prefix-signature-123456"
		directSig = "direct-thought-signature-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:direct-over-prefix"}
	request1 := []byte(`{"sessionId":"direct-over-prefix","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"` + prefixSig + `"},{"text":"hidden","thought":true,"thoughtSignature":"` + directSig + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"direct-over-prefix","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"hidden","thought":true}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != directSig {
		t.Fatalf("thought signature = %q, want direct signature %q; body=%s", got, directSig, out)
	}
	if strings.Contains(string(out), prefixSig) {
		t.Fatalf("unbound prefix must not replace a direct semantic signature; body=%s", out)
	}
}

func TestAntigravityReasoningReplaySameDirectSignatureReplacesPrefix(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "same-prefix-and-direct-signature-123456"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:same-direct-prefix"}
	request1 := []byte(`{"sessionId":"same-direct-prefix","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"` + signature + `"},{"text":"hidden","thought":true,"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"same-direct-prefix","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"hidden","thought":true}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
		t.Fatalf("thought signature = %q, want %q; body=%s", got, signature, out)
	}
}

func TestAntigravityReasoningReplayDirectToolSignatureWinsOverPrefix(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		prefixSig = "unbound-tool-prefix-signature-123456"
		directSig = "direct-tool-signature-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:direct-tool-over-prefix"}
	request1 := []byte(`{"sessionId":"direct-tool-over-prefix","request":{"contents":[{"role":"user","parts":[{"text":"run"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + prefixSig + `"},{"functionCall":{"id":"call-1","name":"run","args":{}},"thoughtSignature":"` + directSig + `"},{"text":"after"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"direct-tool-over-prefix","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run","args":{}}},{"text":"after"}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"run","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != directSig {
		t.Fatalf("tool signature = %q, want %q; body=%s", got, directSig, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "" {
		t.Fatalf("prefix signature retargeted to later text: %q; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayAttachesDetachedSignatureToFunctionCall(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "detached-function-signature-123456789"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:detached-function"}
	request1 := []byte(`{"sessionId":"detached-function","request":{"contents":[{"role":"user","parts":[{"text":"run"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-1","name":"run","args":{"n":1}}},{"text":"","thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"detached-function","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run","args":{"n":1}}}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
		t.Fatalf("function signature = %q, want %q; body=%s", got, signature, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresParallelOmittedCalls(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:parallel-omitted-calls"
	)
	full := []byte(`{"sessionId":"parallel-omitted-calls","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"run","args":{"n":1}},"thoughtSignature":"parallel-call-signature-one-123456"},{"functionCall":{"id":"id2","name":"run","args":{"n":2}},"thoughtSignature":"parallel-call-signature-two-123456"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}},{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]},{"role":"user","parts":[{"text":"finish"}]}]}}`)
	items := antigravityReasoningReplayItemsFromRequest(full)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, items) {
		t.Fatal("cache write failed")
	}

	rebuilt := []byte(`{"sessionId":"parallel-omitted-calls","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}},{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]},{"role":"user","parts":[{"text":"finish"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, rebuilt)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(out); errValidate != nil {
		t.Fatalf("parallel replay is invalid: %v; body=%s", errValidate, out)
	}
	calls := gjson.GetBytes(out, "request.contents.1.parts").Array()
	if len(calls) != 2 || calls[0].Get("functionCall.id").String() != "id1" || calls[1].Get("functionCall.id").String() != "id2" {
		t.Fatalf("parallel calls were not restored together: %s", out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayReordersResponsesAfterRestoringParallelCalls(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:parallel-reversed-responses"
	)
	full := []byte(`{"sessionId":"parallel-reversed-responses","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"run","args":{"n":1}},"thoughtSignature":"parallel-call-signature-one-123456"},{"functionCall":{"id":"id2","name":"run","args":{"n":2}}}]},{"role":"model","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}},{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]}]}}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, antigravityReasoningReplayItemsFromRequest(full)) {
		t.Fatal("cache write failed")
	}

	rebuilt := []byte(`{"sessionId":"parallel-reversed-responses","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"user","parts":[{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}},{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, rebuilt)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(out); errValidate != nil {
		t.Fatalf("restored reverse responses are invalid: %v; body=%s", errValidate, out)
	}
	responses := gjson.GetBytes(out, "request.contents.2.parts").Array()
	if len(responses) != 2 || responses[0].Get("functionResponse.id").String() != "id1" || responses[1].Get("functionResponse.id").String() != "id2" {
		t.Fatalf("restored responses were not reordered: %s", out)
	}
	if got := gjson.GetBytes(out, "request.contents.2.role").String(); got != "model" {
		t.Fatalf("restored response role = %q, want model; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresSyntheticParallelCallsWithFirstBypassOnly(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:parallel-omitted-synthetic-calls"
	)
	full := []byte(`{"sessionId":"parallel-omitted-synthetic-calls","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"run","args":{"n":1}},"thoughtSignature":"skip_thought_signature_validator"},{"functionCall":{"id":"id2","name":"run","args":{"n":2}}}]},{"role":"model","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}},{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]}]}}`)
	items := antigravityReasoningReplayItemsFromRequest(full)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, items) {
		t.Fatal("cache write failed")
	}

	rebuilt := []byte(`{"sessionId":"parallel-omitted-synthetic-calls","request":{"contents":[{"role":"user","parts":[{"text":"run both"}]},{"role":"user","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}},{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]}]}}`)
	rebuilt = sanitizeAntigravityGeminiRequestSignatures(model, rebuilt)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, rebuilt)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	calls := gjson.GetBytes(out, "request.contents.1.parts").Array()
	if len(calls) != 2 {
		t.Fatalf("parallel synthetic calls = %d, want 2; body=%s", len(calls), out)
	}
	if got := calls[0].Get("thoughtSignature").String(); got != "skip_thought_signature_validator" {
		t.Fatalf("first synthetic call signature = %q, want bypass; body=%s", got, out)
	}
	if signature := calls[1].Get("thoughtSignature"); signature.Exists() {
		t.Fatalf("second synthetic parallel call must remain unsigned; body=%s", out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresSequentialOmittedCalls(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:sequential-omitted-calls"
	)
	full := []byte(`{"sessionId":"sequential-omitted-calls","request":{"contents":[{"role":"user","parts":[{"text":"run1"}]},{"role":"model","parts":[{"functionCall":{"id":"id1","name":"run","args":{"n":1}},"thoughtSignature":"omitted-call-signature-one-123456"}]},{"role":"function","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}}]},{"role":"user","parts":[{"text":"run2"}]},{"role":"model","parts":[{"functionCall":{"id":"id2","name":"run","args":{"n":2}},"thoughtSignature":"omitted-call-signature-two-123456"}]},{"role":"function","parts":[{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]}]}}`)
	items := antigravityReasoningReplayItemsFromRequest(full)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, items) {
		t.Fatal("cache write failed")
	}

	rebuilt := []byte(`{"sessionId":"sequential-omitted-calls","request":{"contents":[{"role":"user","parts":[{"text":"run1"}]},{"role":"function","parts":[{"functionResponse":{"id":"id1","name":"run","response":{"result":"one"}}}]},{"role":"user","parts":[{"text":"run2"}]},{"role":"function","parts":[{"functionResponse":{"id":"id2","name":"run","response":{"result":"two"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, rebuilt)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	var calls []string
	gjson.GetBytes(out, "request.contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if callID := part.Get("functionCall.id").String(); callID != "" {
				calls = append(calls, callID)
			}
			return true
		})
		return true
	})
	if got := strings.Join(calls, ","); got != "id1,id2" {
		t.Fatalf("restored calls = %q, want id1,id2; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayAccumulatesCompleteToolSignatureChain(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model      = "gemini-3.6-flash-high"
		sessionKey = "session:tool-chain"
		sig1       = "native-tool-signature-one-123456"
		sig2       = "native-tool-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: model, sessionKey: sessionKey}
	request1 := []byte(`{"sessionId":"tool-chain","request":{"contents":[{"role":"user","parts":[{"text":"run first"}]}]}}`)
	acc1 := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc1.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig1 + `","functionCall":{"id":"call-1","name":"run_command","args":{"command":"one"}}}]},"finishReason":"STOP"}]}}`))
	acc1.Commit(context.Background())

	request2 := []byte(`{"sessionId":"tool-chain","request":{"contents":[{"role":"user","parts":[{"text":"run first"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"one"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run_command","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"run second"}]}]}}`)
	request2 = normalizeAntigravityGeminiFunctionResponseRoles(request2)
	prepared2, _, errPrepare2 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare2 != nil {
		t.Fatal(errPrepare2)
	}
	if got := gjson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 2 tool signature = %q, want %q; body=%s", got, sig1, prepared2)
	}

	acc2 := newAntigravityReasoningReplayAccumulator(scope, prepared2)
	acc2.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig2 + `","functionCall":{"id":"call-2","name":"view_file","args":{"path":"two"}}}]},"finishReason":"STOP"}]}}`))
	acc2.Commit(context.Background())

	request3 := []byte(`{"sessionId":"tool-chain","request":{"contents":[{"role":"user","parts":[{"text":"run first"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"one"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run_command","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"run second"}]},{"role":"model","parts":[{"functionCall":{"id":"call-2","name":"view_file","args":{"path":"two"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"call-2","name":"view_file","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"finish"}]}]}}`)
	request3 = normalizeAntigravityGeminiFunctionResponseRoles(request3)
	prepared3, _, errPrepare3 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request3)
	if errPrepare3 != nil {
		t.Fatal(errPrepare3)
	}
	if got := gjson.GetBytes(prepared3, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 3 first tool signature = %q, want %q; body=%s", got, sig1, prepared3)
	}
	if got := gjson.GetBytes(prepared3, "request.contents.4.parts.0.thoughtSignature").String(); got != sig2 {
		t.Fatalf("turn 3 second tool signature = %q, want %q; body=%s", got, sig2, prepared3)
	}
}

func TestAntigravityReasoningReplayDirectSignatureClosesSegmentBeforeUnsignedText(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "direct-text-signature-closes-segment-123456"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:direct-signed-unsigned"}
	request1 := []byte(`{"sessionId":"direct-signed-unsigned","request":{"contents":[{"role":"user","parts":[{"text":"start"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"signed","thoughtSignature":"` + signature + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"unsigned"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"direct-signed-unsigned","request":{"contents":[{"role":"user","parts":[{"text":"start"}]},{"role":"model","parts":[{"text":"signed"},{"text":"unsigned"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	parts := gjson.GetBytes(out, "request.contents.1.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != signature || parts[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("direct signature crossed into unsigned text: %s", out)
	}
}

func TestAntigravityReasoningReplayKeepsMixedTextToolTextFingerprintsSeparate(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		textSig1 = "mixed-text-signature-one-123456"
		toolSig  = "mixed-tool-signature-123456789"
		textSig2 = "mixed-text-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:mixed-segments"}
	request1 := []byte(`{"sessionId":"mixed-segments","request":{"contents":[{"role":"user","parts":[{"text":"mixed"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"sa"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"me","thoughtSignature":"` + textSig1 + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"true"}},"thoughtSignature":"` + toolSig + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"sa"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"me","thoughtSignature":"` + textSig2 + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey)
	if !ok || len(items) != 3 {
		t.Fatalf("cached mixed items = %d ok=%v, want 3", len(items), ok)
	}
	if occurrence := gjson.GetBytes(items[0], "targetOccurrence"); !occurrence.Exists() || occurrence.Int() != 0 {
		t.Fatalf("first text targetOccurrence = %s, want 0; item=%s", occurrence.Raw, items[0])
	}
	if got := gjson.GetBytes(items[2], "targetOccurrence").Int(); got != 1 {
		t.Fatalf("second text targetOccurrence = %d, want 1; item=%s", got, items[2])
	}

	request2 := []byte(`{"sessionId":"mixed-segments","request":{"contents":[{"role":"user","parts":[{"text":"mixed"}]},{"role":"model","parts":[{"text":"same"},{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"true"}}},{"text":"same"}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run_command","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	for partIndex, want := range []string{textSig1, toolSig, textSig2} {
		if got := gjson.GetBytes(out, fmt.Sprintf("request.contents.1.parts.%d.thoughtSignature", partIndex)).String(); got != want {
			t.Fatalf("part %d signature = %q, want %q; body=%s", partIndex, got, want, out)
		}
	}
}

func TestAntigravityReasoningReplayAccumulatorCountsExistingSegmentOccurrences(t *testing.T) {
	request := []byte(`{"request":{"contents":[{"role":"model","parts":[{"text":"same"},{"text":"same","thought":true}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(
		antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:existing-segments"},
		request,
	)
	acc.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"same","thoughtSignature":"text-signature-123456789"},{"text":"same","thought":true,"thoughtSignature":"thought-signature-123456789"}]},"finishReason":"STOP"}]}}`))
	acc.appendPendingThoughtSignatures()
	if len(acc.items) != 2 {
		t.Fatalf("captured items = %d, want 2: %q", len(acc.items), acc.items)
	}
	for itemIndex, wantKind := range []string{"text", "thought"} {
		item := gjson.ParseBytes(acc.items[itemIndex])
		if item.Get("targetKind").String() != wantKind || item.Get("targetOccurrence").Int() != 1 {
			t.Fatalf("item %d kind/occurrence = %q/%d, want %q/1: %s", itemIndex, item.Get("targetKind").String(), item.Get("targetOccurrence").Int(), wantKind, item.Raw)
		}
	}
}

func TestAntigravityReasoningReplayAccumulatorCountsExistingFunctionOccurrenceThroughReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "second-function-signature-123456789"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:existing-function"}
	request := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run","args":{"value":"same"}}}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request)
	acc.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"run","args":{"value":"same"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey)
	if !ok || len(items) != 2 || gjson.GetBytes(items[1], "targetOccurrence").Int() != 1 {
		t.Fatalf("function occurrences were not committed: ok=%v items=%q", ok, items)
	}
	replayPayload := []byte(`{"sessionId":"existing-function","request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run","args":{"value":"same"}}},{"functionCall":{"name":"run","args":{"value":"same"}}}]}]}}`)
	prepared, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, replayPayload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	parts := gjson.GetBytes(prepared, "request.contents.0.parts").Array()
	if len(parts) != 2 || parts[0].Get("thoughtSignature").String() != "" || parts[1].Get("thoughtSignature").String() != signature {
		t.Fatalf("function occurrence replay targeted the wrong call: %s", prepared)
	}
}

func TestAntigravityReasoningReplayCapturesSignatureBeforeThoughtText(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "thought-first-signature-123456789"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:thought-first"}
	request1 := []byte(`{"sessionId":"thought-first","request":{"contents":[{"role":"user","parts":[{"text":"think"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thought":true,"thoughtSignature":"` + signature + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hidden thought","thought":true}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"thought-first","request":{"contents":[{"role":"user","parts":[{"text":"think"}]},{"role":"model","parts":[{"text":"hidden thought","thought":true}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
		t.Fatalf("thought-first signature = %q, want %q; body=%s", got, signature, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayReplacesIDLessFunctionCallBypass(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"run_command","args":{"command":"same"},"thoughtSignature":"idless-native-signature-123456"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:idless", [][]byte{item})
	payload := []byte(`{"sessionId":"idless","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}},"thoughtSignature":"skip_thought_signature_validator"}]},{"role":"function","parts":[{"functionResponse":{"name":"run_command","response":{"result":"ok"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "idless-native-signature-123456" {
		t.Fatalf("id-less function signature = %q, want native replay; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayContextFingerprintCanonicalizesJSON(t *testing.T) {
	payload1 := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"run","parameters":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}}]}],"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"functionCall":{"name":"run","args":{"a":"x","b":2}}}]}]}}`)
	payload2 := []byte(`{"request":{"tools":[{"functionDeclarations":[{"parameters":{"properties":{"b":{"type":"number"},"a":{"type":"string"}},"type":"object"},"name":"run"}]}],"contents":[{"parts":[{"text":"turn"}],"role":"user"},{"parts":[{"functionCall":{"args":{"b":2,"a":"x"},"name":"run"}}],"role":"model"}]}}`)
	if got1, got2 := antigravityReplayContextFingerprint(payload1, 2), antigravityReplayContextFingerprint(payload2, 2); got1 == "" || got1 != got2 {
		t.Fatalf("canonical context hashes differ: %q vs %q", got1, got2)
	}
	key1 := antigravityFunctionCallKey("run", `{"a":"x","b":2}`, "")
	key2 := antigravityFunctionCallKey("run", `{"b":2,"a":"x"}`, "")
	if key1 == "" || key1 != key2 {
		t.Fatalf("canonical function keys differ: %q vs %q", key1, key2)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayMatchesRewrittenToolCallID(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"call_id":"native-call-id","name":"run_command","args":{"command":"same"},"thoughtSignature":"rewritten-id-signature-123456"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:rewritten-id", [][]byte{item})
	payload := []byte(`{"sessionId":"rewritten-id","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"id":"claude-generated-id","name":"run_command","args":{"command":"same"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"claude-generated-id","name":"run_command","response":{"result":"ok"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "rewritten-id-signature-123456" {
		t.Fatalf("rewritten-ID function signature = %q, want native replay; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsReusedIDWithChangedCall(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"call_id":"reused-id","name":"run_command","args":{"command":"old"},"thoughtSignature":"reused-id-stale-signature-123456"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:reused-id", [][]byte{item})
	payload := []byte(`{"sessionId":"reused-id","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"id":"reused-id","name":"run_command","args":{"command":"new"}}},{"functionCall":{"id":"other-id","name":"run_command","args":{"command":"old"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"reused-id","name":"run_command","response":{"result":"ok"}}},{"functionResponse":{"id":"other-id","name":"run_command","response":{"result":"ok"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("changed call with reused ID received stale signature %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "" {
		t.Fatalf("reused-ID signature fell through to another semantic match %q; body=%s", got, out)
	}
	callCount := 0
	gjson.GetBytes(out, "request.contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall.id").String() == "reused-id" {
				callCount++
			}
			return true
		})
		return true
	})
	if callCount != 1 {
		t.Fatalf("changed call with reused ID was duplicated: count=%d body=%s", callCount, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsChangedIDLessCallAtSamePosition(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"name":"run_command","args":{"command":"old"},"thoughtSignature":"idless-stale-signature-123456"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:idless-changed", [][]byte{item})
	payload := []byte(`{"sessionId":"idless-changed","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"new"}}}]},{"role":"function","parts":[{"functionResponse":{"name":"run_command","response":{"result":"ok"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("changed ID-less call received stale signature %q; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayPreservesRepeatedIDLessCalls(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "repeated-idless-signature-one-123456"
		sig2 = "repeated-idless-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:repeated-idless"}
	request1 := []byte(`{"sessionId":"repeated-idless","request":{"contents":[{"role":"user","parts":[{"text":"run twice"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig1 + `","functionCall":{"name":"run_command","args":{"command":"same"}}},{"thoughtSignature":"` + sig2 + `","functionCall":{"name":"run_command","args":{"command":"same"}}}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"repeated-idless","request":{"contents":[{"role":"user","parts":[{"text":"run twice"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}}},{"functionCall":{"name":"run_command","args":{"command":"same"}}}]},{"role":"model","parts":[{"functionResponse":{"name":"run_command","response":{"result":"one"}}},{"functionResponse":{"name":"run_command","response":{"result":"two"}}}]},{"role":"user","parts":[{"text":"continue"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first repeated signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second repeated signature = %q, want %q; body=%s", got, sig2, out)
	}
}

func TestAntigravityReasoningReplayPreservesRepeatedIDLessCallsAcrossSplitSSEPartDrift(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		sig1 = "split-idless-signature-one-123456"
		sig2 = "split-idless-signature-two-123456"
	)
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:split-repeated-idless"}
	request1 := []byte(`{"sessionId":"split-repeated-idless","request":{"contents":[{"role":"user","parts":[{"text":"run twice"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"hidden","thought":true}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig1 + `","functionCall":{"name":"run_command","args":{"command":"same"}}}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"` + sig2 + `","functionCall":{"name":"run_command","args":{"command":"same"}}}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey)
	if !ok || len(items) != 2 {
		t.Fatalf("cached items = %d ok=%v, want 2", len(items), ok)
	}
	for index, item := range items {
		if occurrence := gjson.GetBytes(item, "targetOccurrence"); !occurrence.Exists() || occurrence.Int() != int64(index) {
			t.Fatalf("item %d occurrence = %s, want %d; item=%s", index, occurrence.Raw, index, item)
		}
	}

	request2 := []byte(`{"sessionId":"split-repeated-idless","request":{"contents":[{"role":"user","parts":[{"text":"run twice"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}}},{"functionCall":{"name":"run_command","args":{"command":"same"}}}]},{"role":"model","parts":[{"functionResponse":{"name":"run_command","response":{"result":"one"}}},{"functionResponse":{"name":"run_command","response":{"result":"two"}}}]},{"role":"user","parts":[{"text":"continue"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first split repeated signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second split repeated signature = %q, want %q; body=%s", got, sig2, out)
	}

	rebuiltItems := antigravityReasoningReplayItemsFromRequest(out)
	if len(rebuiltItems) != 2 || gjson.GetBytes(rebuiltItems[0], "targetOccurrence").Int() != 0 || gjson.GetBytes(rebuiltItems[1], "targetOccurrence").Int() != 1 {
		t.Fatalf("rebuilt occurrences were not preserved: %q", rebuiltItems)
	}
}

func TestAntigravityReasoningReplayLegacyAmbiguousIDLessCallFailsClosed(t *testing.T) {
	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":1,"name":"run_command","args":{"command":"same"},"thoughtSignature":"legacy-ambiguous-signature-123456"}`)
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}}},{"functionCall":{"name":"run_command","args":{"command":"same"}}}]}]}}`)
	out, changed := insertAntigravityReasoningReplayItems(payload, [][]byte{item})
	if changed || strings.Contains(string(out), "legacy-ambiguous-signature") {
		t.Fatalf("legacy ambiguous ID-less replay must fail closed: changed=%v body=%s", changed, out)
	}
}

func TestAntigravityReasoningReplayAssociatesSignatureBeforeFunctionCall(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const signature = "signature-before-function-call-123456"
	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:signature-first-tool"}
	request1 := []byte(`{"sessionId":"signature-first-tool","request":{"contents":[{"role":"user","parts":[{"text":"run"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request1)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"` + signature + `"}]}}]}}`))
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"one"}}}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())

	request2 := []byte(`{"sessionId":"signature-first-tool","request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"one"}}}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run_command","response":{"result":"ok"}}}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
		t.Fatalf("signature-first tool signature = %q, want %q; body=%s", got, signature, out)
	}
}

func TestAntigravityReasoningReplayTerminalEmptyChainClearsCache(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:empty-reset"}
	old := []byte(`{"type":"thought_signature","contentIndex":1,"partIndex":0,"thoughtSignature":"old-signature-123456789"}`)
	internalcache.CacheAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey, [][]byte{old})

	request := []byte(`{"sessionId":"empty-reset","request":{"contents":[{"role":"user","parts":[{"text":"new conversation"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"answer without signature"}]},"finishReason":"STOP"}]}}`))
	acc.Commit(context.Background())
	if items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey); ok || len(items) != 0 {
		t.Fatalf("empty terminal chain did not clear old cache: %d ok=%v", len(items), ok)
	}
}

func TestAntigravityReasoningReplayDoesNotCommitPartialResponse(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	scope := antigravityReasoningReplayScope{modelName: "gemini-3.6-flash-high", sessionKey: "session:partial"}
	request := []byte(`{"sessionId":"partial","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, request)
	acc.ObserveSSELine([]byte(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial"},{"text":"","thoughtSignature":"partial-signature-123456789"}]}}]}}`))
	acc.Commit(context.Background())

	if items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey); ok || len(items) != 0 {
		t.Fatalf("partial response published replay items: %d ok=%v", len(items), ok)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsFingerprintMismatch(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	kind, fingerprint := antigravityReplayPartFingerprint(gjson.Parse(`{"text":"same answer"}`))
	item := buildAntigravityThoughtSignatureItem(1, 0, "fingerprinted-signature-123456", kind, fingerprint)
	originalPayload := []byte(`{"sessionId":"rebuilt","request":{"contents":[{"role":"user","parts":[{"text":"old context"}]},{"role":"model","parts":[{"text":"same answer"}]},{"role":"user","parts":[{"text":"old next"}]}]}}`)
	item = antigravitySetReplayItemContextHash(item, originalPayload, 1)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:rebuilt", [][]byte{item})

	payload := []byte(`{"sessionId":"rebuilt","request":{"contents":[{"role":"user","parts":[{"text":"new context"}]},{"role":"model","parts":[{"text":"same answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("mismatched rebuilt context received stale signature %q; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayMovesClientSignatureToNativePart(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	kind, fingerprint := antigravityReplayPartFingerprint(gjson.Parse(`{"text":"visible answer"}`))
	item := buildAntigravityThoughtSignatureItem(1, 1, "client-carried-signature-123456", kind, fingerprint)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:client-carried", [][]byte{item})
	payload := []byte(`{"sessionId":"client-carried","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"hidden","thought":true,"thoughtSignature":"client-carried-signature-123456"},{"text":"visible answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("client-carried signature remained on non-native thought part: %q; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "client-carried-signature-123456" {
		t.Fatalf("client-carried signature = %q on visible part, want native placement; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayReplacesBypassSignature(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	item := []byte(`{"type":"thought_signature","contentIndex":1,"partIndex":0,"thoughtSignature":"native-real-signature-123456"}`)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:bypass", [][]byte{item})
	payload := []byte(`{"sessionId":"bypass","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"answer","thoughtSignature":"skip_thought_signature_validator"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "native-real-signature-123456" {
		t.Fatalf("signature = %q, want native replay; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayScopePrefersExecutionSession(t *testing.T) {
	req := cliproxyexecutor.Request{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "client-session"}}
	payload := []byte(`{"sessionId":"provider-session","request":{"contents":[{"role":"user","parts":[{"text":"same prompt"}]}]}}`)
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", req, cliproxyexecutor.Options{}, payload)
	if got := scope.sessionKey; got != "execution:client-session" {
		t.Fatalf("session key = %q, want downstream execution session", got)
	}
}

func TestAntigravityReasoningReplayScopePrefersStableSessionOverExecutionUUID(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"Session-Id": []string{"stable-session"}},
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "socket-uuid"},
	}
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, nil)
	if got := scope.sessionKey; got != "responses:stable-session" {
		t.Fatalf("session key = %q, want stable Responses session", got)
	}
}

func TestAntigravityReasoningReplayScopeKeepsExecutionAheadOfPromptCacheKey(t *testing.T) {
	opts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"prompt_cache_key":"shared-cache-bucket"}`),
		Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "socket-session"},
	}
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, nil)
	if got := scope.sessionKey; got != "execution:socket-session" {
		t.Fatalf("session key = %q, want execution scope ahead of prompt cache key", got)
	}
}

func TestAntigravityReasoningReplayScopeSeparatesPromptCacheAndExplicitSessionNamespaces(t *testing.T) {
	promptScope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"same-value"}`)}, nil)
	sessionScope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{OriginalRequest: []byte(`{"session_id":"same-value"}`)}, nil)
	if promptScope.sessionKey != "prompt-cache:same-value" || sessionScope.sessionKey != "responses:same-value" || promptScope.sessionKey == sessionScope.sessionKey {
		t.Fatalf("prompt/session namespaces collided: %q vs %q", promptScope.sessionKey, sessionScope.sessionKey)
	}
}

func TestAntigravityReasoningReplayScopeUsesClaudeMetadataSession(t *testing.T) {
	opts := cliproxyexecutor.Options{OriginalRequest: []byte(`{"metadata":{"user_id":"{\"session_id\":\"claude-session\",\"device_id\":\"device\"}"}}`)}
	payload := []byte(`{"sessionId":"generated-from-prompt","request":{"contents":[{"role":"user","parts":[{"text":"same prompt"}]}]}}`)
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, payload)
	if got := scope.sessionKey; got != "claude:claude-session:agent:main" {
		t.Fatalf("session key = %q, want Claude root-agent session", got)
	}
}

func TestAntigravityReasoningReplaySeparatesClaudeSessionTitleFromResumedTranscript(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model = "gemini-3.6-flash-high"
		sig1  = "claude-resume-signature-one-123456"
		sig2  = "claude-resume-signature-two-123456"
	)
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"claude-resume-session"}}
	mainOriginal1 := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"root prompt"}]}],"system":[{"type":"text","text":"You are Claude Code."}],"thinking":{"type":"enabled"},"tools":[{"name":"Read"}]}`)
	mainRequest1 := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"root prompt"}]}]}}`)
	mainScope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Headers: headers, OriginalRequest: mainOriginal1}, mainRequest1)
	mainAccumulator1 := newAntigravityReasoningReplayAccumulator(mainScope, mainRequest1)
	mainAccumulator1.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"answer one"},{"text":"","thoughtSignature":"` + sig1 + `"}]},"finishReason":"STOP"}]}}`))
	mainAccumulator1.Commit(context.Background())

	// Prepare the next main turn before the auxiliary request commits. This
	// reproduces Claude Code's concurrent title/main request ordering.
	mainOriginal2 := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"root prompt"}]},{"role":"assistant","content":[{"type":"text","text":"answer one"}]},{"role":"user","content":[{"type":"text","text":"next prompt"}]}],"system":[{"type":"text","text":"You are Claude Code."}],"thinking":{"type":"enabled"},"tools":[{"name":"Read"}]}`)
	mainRequest2 := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"root prompt"}]},{"role":"model","parts":[{"text":"answer one"}]},{"role":"user","parts":[{"text":"next prompt"}]}]}}`)
	prepared2, scope2, errPrepare2 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Headers: headers, OriginalRequest: mainOriginal2}, mainRequest2)
	if errPrepare2 != nil {
		t.Fatal(errPrepare2)
	}
	if scope2.sessionKey != mainScope.sessionKey {
		t.Fatalf("main replay scope changed: first=%q second=%q", mainScope.sessionKey, scope2.sessionKey)
	}

	titleOriginal := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"root prompt"}]}],"system":[{"type":"text","text":"Generate a concise title that summarizes this session."}],"tools":[]}`)
	titleRequest := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"root prompt"}]}]}}`)
	preparedTitle, titleScope, errPrepareTitle := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Headers: headers, OriginalRequest: titleOriginal}, titleRequest)
	if errPrepareTitle != nil {
		t.Fatal(errPrepareTitle)
	}
	if titleScope.sessionKey == mainScope.sessionKey || !strings.Contains(mainScope.sessionKey, ":context:") || !strings.Contains(titleScope.sessionKey, ":context:") {
		t.Fatalf("Claude title/main replay scopes collided: main=%q title=%q", mainScope.sessionKey, titleScope.sessionKey)
	}
	titleAccumulator := newAntigravityReasoningReplayAccumulator(titleScope, preparedTitle)
	titleAccumulator.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"session title"},{"text":"","thoughtSignature":"claude-title-signature-123456"}]},"finishReason":"STOP"}]}}`))
	titleAccumulator.Commit(context.Background())

	if got := gjson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first main signature after title request = %q, want %q; body=%s", got, sig1, prepared2)
	}
	mainAccumulator2 := newAntigravityReasoningReplayAccumulator(scope2, prepared2)
	mainAccumulator2.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"answer two"},{"text":"","thoughtSignature":"` + sig2 + `"}]},"finishReason":"STOP"}]}}`))
	mainAccumulator2.Commit(context.Background())

	resumeOriginal := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"root prompt"}]},{"role":"assistant","content":[{"type":"text","text":"answer one"}]},{"role":"user","content":[{"type":"text","text":"next prompt"}]},{"role":"assistant","content":[{"type":"text","text":"answer two"}]},{"role":"user","content":[{"type":"text","text":"resumed prompt"}]}],"system":[{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"enabled"},"tools":[{"name":"Read"}]}`)
	resumeRequest := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"root prompt"}]},{"role":"model","parts":[{"text":"answer one"}]},{"role":"user","parts":[{"text":"next prompt"}]},{"role":"model","parts":[{"text":"answer two"}]},{"role":"user","parts":[{"text":"resumed prompt"}]}]}}`)
	resumed, resumeScope, errResume := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Headers: headers, OriginalRequest: resumeOriginal}, resumeRequest)
	if errResume != nil {
		t.Fatal(errResume)
	}
	if resumeScope.sessionKey != mainScope.sessionKey {
		t.Fatalf("resumed replay scope = %q, want %q", resumeScope.sessionKey, mainScope.sessionKey)
	}
	if got := gjson.GetBytes(resumed, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("resumed first signature = %q, want %q; body=%s", got, sig1, resumed)
	}
	if got := gjson.GetBytes(resumed, "request.contents.3.parts.0.thoughtSignature").String(); got != sig2 {
		t.Fatalf("resumed second signature = %q, want %q; body=%s", got, sig2, resumed)
	}
}

func TestAntigravityReasoningReplayScopeSeparatesClaudeAgents(t *testing.T) {
	opts := cliproxyexecutor.Options{Headers: http.Header{
		"X-Claude-Code-Session-Id": []string{"claude-session"},
		"X-Claude-Code-Agent-Id":   []string{"subagent-1"},
	}}
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, nil)
	if got := scope.sessionKey; got != "claude:claude-session:agent:subagent-1" {
		t.Fatalf("session key = %q, want agent-scoped Claude session", got)
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
