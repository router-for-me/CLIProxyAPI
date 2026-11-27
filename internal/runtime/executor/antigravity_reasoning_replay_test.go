package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorgeminigemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
	pi0 := int(ojson.GetBytes(items[0], "partIndex").Int())
	pi1 := int(ojson.GetBytes(items[1], "partIndex").Int())
	if pi0 != 0 || pi1 != 1 {
		t.Fatalf("partIndex = %d,%d, want 0,1", pi0, pi1)
	}
	if got := ojson.GetBytes(items[0], "thoughtSignature").String(); got != "sig-first" {
		t.Fatalf("first sig = %q", got)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayPayloadToleratesHomeKVFailure(t *testing.T) {
	// An enabled Home client with no heartbeat makes CurrentKVClient report home
	// mode with an error, which is how every Home-side KV failure reaches the
	// replay cache — including the "unknown command 'cas'" case from an older
	// Home. The request must proceed without replay rather than fail, because a
	// bare executor error would make MarkResult mark the credential unavailable.
	homekv.SetCurrent(homekv.New(config.HomeConfig{Enabled: true}))
	t.Cleanup(func() { homekv.SetCurrent(nil) })

	payload := []byte(`{"sessionId":"kv-failure","request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3-flash-agent", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatalf("prepare error = %v, want nil so the request proceeds without replay", errPrepare)
	}
	if len(out) == 0 {
		t.Fatal("prepare returned an empty payload")
	}
	if got := ojson.GetBytes(out, "sessionId").String(); got != "kv-failure" {
		t.Fatalf("payload sessionId = %q, want kv-failure", got)
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
	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"answer"}`))
	item := buildAntigravityThoughtSignatureItem(0, 0, "valid-cache-signature-123456789", kind, fingerprint)
	item = antigravityReplayItemContextHashForTest(item, payload, 0)
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
	if ojson.GetBytes(out, "request.contents.1.role").String() != "model" {
		t.Fatalf("functionCall replay must be model role at [1], got %s", string(out))
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "sig-first" {
		t.Fatalf("thoughtSignature = %q, want sig-first", got)
	}
	if !ojson.GetBytes(out, "request.contents.1.parts.0.functionCall").Exists() {
		t.Fatalf("functionCall not injected: %s", string(out))
	}
	if !ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse").Exists() {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "skip_thought_signature_validator" {
		t.Fatalf("inserted first synthetic functionCall signature = %q, want bypass sentinel; output=%s", got, out)
	}
	if got := ojson.GetBytes(out, "request.contents.2.role").String(); got != "model" {
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
	if !ojson.GetBytes(out, "request.contents.1.parts.0.functionCall").Exists() || ojson.GetBytes(out, "request.contents.1.role").String() != "model" {
		t.Fatalf("want model functionCall at [1]: %s", string(out))
	}
	if !ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse").Exists() {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "sig-first" {
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

	parts := ojson.GetBytes(out, "request.contents.1.parts").Array()
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
	if got := ojson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 2 signature = %q, want %q; body=%s", got, sig1, prepared2)
	}
	if got := ojson.GetBytes(prepared2, "request.contents.1.parts.#").Int(); got != 1 {
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
	if got := ojson.GetBytes(prepared3, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 3 first signature = %q, want %q; body=%s", got, sig1, prepared3)
	}
	if got := ojson.GetBytes(prepared3, "request.contents.3.parts.0.thoughtSignature").String(); got != sig2 {
		t.Fatalf("turn 3 second signature = %q, want %q; body=%s", got, sig2, prepared3)
	}
	if got := len(ojson.GetBytes(prepared3, "request.contents.1.parts").Array()) + len(ojson.GetBytes(prepared3, "request.contents.3.parts").Array()); got != 2 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first consecutive text signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first trailing text signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig3 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first thought signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != directSig {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != directSig {
		t.Fatalf("tool signature = %q, want %q; body=%s", got, directSig, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "" {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
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
	calls := ojson.GetBytes(out, "request.contents.1.parts").Array()
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
	responses := ojson.GetBytes(out, "request.contents.2.parts").Array()
	if len(responses) != 2 || responses[0].Get("functionResponse.id").String() != "id1" || responses[1].Get("functionResponse.id").String() != "id2" {
		t.Fatalf("restored responses were not reordered: %s", out)
	}
	if got := ojson.GetBytes(out, "request.contents.2.role").String(); got != "model" {
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
	calls := ojson.GetBytes(out, "request.contents.1.parts").Array()
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
	ojson.GetBytes(out, "request.contents").ForEach(func(_, content ojson.Result) bool {
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
	if got := ojson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
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
	if got := ojson.GetBytes(prepared3, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("turn 3 first tool signature = %q, want %q; body=%s", got, sig1, prepared3)
	}
	if got := ojson.GetBytes(prepared3, "request.contents.4.parts.0.thoughtSignature").String(); got != sig2 {
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
	parts := ojson.GetBytes(out, "request.contents.1.parts").Array()
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
	if occurrence := ojson.GetBytes(items[0], "targetOccurrence"); !occurrence.Exists() || occurrence.Int() != 0 {
		t.Fatalf("first text targetOccurrence = %s, want 0; item=%s", occurrence.Raw, items[0])
	}
	if got := ojson.GetBytes(items[2], "targetOccurrence").Int(); got != 1 {
		t.Fatalf("second text targetOccurrence = %d, want 1; item=%s", got, items[2])
	}

	request2 := []byte(`{"sessionId":"mixed-segments","request":{"contents":[{"role":"user","parts":[{"text":"mixed"}]},{"role":"model","parts":[{"text":"same"},{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"true"}}},{"text":"same"}]},{"role":"function","parts":[{"functionResponse":{"id":"call-1","name":"run_command","response":{"result":"ok"}}}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	for partIndex, want := range []string{textSig1, toolSig, textSig2} {
		if got := ojson.GetBytes(out, fmt.Sprintf("request.contents.1.parts.%d.thoughtSignature", partIndex)).String(); got != want {
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
		item := ojson.ParseBytes(acc.items[itemIndex])
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
	if !ok || len(items) != 2 || ojson.GetBytes(items[1], "targetOccurrence").Int() != 1 {
		t.Fatalf("function occurrences were not committed: ok=%v items=%q", ok, items)
	}
	replayPayload := []byte(`{"sessionId":"existing-function","request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"run","args":{"value":"same"}}},{"functionCall":{"name":"run","args":{"value":"same"}}}]}]}}`)
	prepared, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, replayPayload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	// The leading call gets Gemini's bypass sentinel (it carries no native
	// signature); only the second occurrence may receive the replayed one.
	parts := ojson.GetBytes(prepared, "request.contents.0.parts").Array()
	if len(parts) != 2 || antigravityHasNativeThoughtSignature(parts[0].Get("thoughtSignature").String()) || parts[1].Get("thoughtSignature").String() != signature {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "idless-native-signature-123456" {
		t.Fatalf("id-less function signature = %q, want native replay; body=%s", got, out)
	}
}

func TestAntigravityReasoningReplayContextFingerprintCanonicalizesJSON(t *testing.T) {
	payload1 := []byte(`{"request":{"tools":[{"functionDeclarations":[{"name":"run","parameters":{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}}]}],"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"functionCall":{"name":"run","args":{"a":"x","b":2}}}]}]}}`)
	payload2 := []byte(`{"request":{"tools":[{"functionDeclarations":[{"parameters":{"properties":{"b":{"type":"number"},"a":{"type":"string"}},"type":"object"},"name":"run"}]}],"contents":[{"parts":[{"text":"turn"}],"role":"user"},{"parts":[{"functionCall":{"args":{"b":2,"a":"x"},"name":"run"}}],"role":"model"}]}}`)
	if got1, got2 := newAntigravityReplayRequestIndex(payload1).contextFingerprint(2), newAntigravityReplayRequestIndex(payload2).contextFingerprint(2); got1 == "" || got1 != got2 {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "rewritten-id-signature-123456" {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); antigravityHasNativeThoughtSignature(got) {
		t.Fatalf("changed call with reused ID received stale signature %q; body=%s", got, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "" {
		t.Fatalf("reused-ID signature fell through to another semantic match %q; body=%s", got, out)
	}
	callCount := 0
	ojson.GetBytes(out, "request.contents").ForEach(func(_, content ojson.Result) bool {
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); antigravityHasNativeThoughtSignature(got) {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first repeated signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
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
		if occurrence := ojson.GetBytes(item, "targetOccurrence"); !occurrence.Exists() || occurrence.Int() != int64(index) {
			t.Fatalf("item %d occurrence = %s, want %d; item=%s", index, occurrence.Raw, index, item)
		}
	}

	request2 := []byte(`{"sessionId":"split-repeated-idless","request":{"contents":[{"role":"user","parts":[{"text":"run twice"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}}},{"functionCall":{"name":"run_command","args":{"command":"same"}}}]},{"role":"model","parts":[{"functionResponse":{"name":"run_command","response":{"result":"one"}}},{"functionResponse":{"name":"run_command","response":{"result":"two"}}}]},{"role":"user","parts":[{"text":"continue"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), scope.modelName, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, request2)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("first split repeated signature = %q, want %q; body=%s", got, sig1, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != sig2 {
		t.Fatalf("second split repeated signature = %q, want %q; body=%s", got, sig2, out)
	}

	rebuiltItems := antigravityReasoningReplayItemsFromRequest(out)
	if len(rebuiltItems) != 2 || ojson.GetBytes(rebuiltItems[0], "targetOccurrence").Int() != 0 || ojson.GetBytes(rebuiltItems[1], "targetOccurrence").Int() != 1 {
		t.Fatalf("rebuilt occurrences were not preserved: %q", rebuiltItems)
	}
}

func TestAntigravityReasoningReplayLegacyAmbiguousIDLessCallFailsClosed(t *testing.T) {
	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":1,"name":"run_command","args":{"command":"same"},"thoughtSignature":"legacy-ambiguous-signature-123456"}`)
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"run"}]},{"role":"model","parts":[{"functionCall":{"name":"run_command","args":{"command":"same"}}},{"functionCall":{"name":"run_command","args":{"command":"same"}}}]}]}}`)
	out, changed := insertAntigravityReasoningReplayItemsWithSchemas(newAntigravityReplayRequestIndex(payload), payload, [][]byte{item}, nil)
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != signature {
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

func TestPrepareAntigravityGeminiReasoningReplayKeepsTextSignatureOnContextDrift(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"same answer"}`))
	item := buildAntigravityThoughtSignatureItem(1, 0, "fingerprinted-signature-123456", kind, fingerprint)
	originalPayload := []byte(`{"sessionId":"rebuilt","request":{"contents":[{"role":"user","parts":[{"text":"old context"}]},{"role":"model","parts":[{"text":"same answer"}]},{"role":"user","parts":[{"text":"old next"}]}]}}`)
	item = antigravityReplayItemContextHashForTest(item, originalPayload, 1)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:rebuilt", [][]byte{item})

	payload := []byte(`{"sessionId":"rebuilt","request":{"contents":[{"role":"user","parts":[{"text":"new context"}]},{"role":"model","parts":[{"text":"same answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	// The signed part itself is byte-identical, so the signature still describes
	// it exactly. Only the surrounding turns drifted, which Gemini does not bind
	// signatures to, so dropping it here would only force needless re-reasoning.
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "fingerprinted-signature-123456" {
		t.Fatalf("signature = %q, want the signature replayed even though the surrounding context drifted; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsFingerprintMismatch(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"original answer"}`))
	item := buildAntigravityThoughtSignatureItem(1, 0, "fingerprinted-signature-123456", kind, fingerprint)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:edited", [][]byte{item})

	// The client rewrote the signed part, so the cached signature describes text
	// that is no longer in the request and must not be attached to the new text.
	payload := []byte(`{"sessionId":"edited","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"edited answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("edited part received stale signature %q; body=%s", got, out)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayMovesClientSignatureToNativePart(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	kind, fingerprint := antigravityReplayPartFingerprint(ojson.Parse(`{"text":"visible answer"}`))
	item := buildAntigravityThoughtSignatureItem(1, 1, "client-carried-signature-123456", kind, fingerprint)
	internalcache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "session:client-carried", [][]byte{item})
	payload := []byte(`{"sessionId":"client-carried","request":{"contents":[{"role":"user","parts":[{"text":"turn"}]},{"role":"model","parts":[{"text":"hidden","thought":true,"thoughtSignature":"client-carried-signature-123456"},{"text":"visible answer"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, payload)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "" {
		t.Fatalf("client-carried signature remained on non-native thought part: %q; body=%s", got, out)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.1.thoughtSignature").String(); got != "client-carried-signature-123456" {
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
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String(); got != "native-real-signature-123456" {
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

func TestAntigravityReasoningReplayScopeSupportsUnderscoreSessionIDHeader(t *testing.T) {
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"Session_id": []string{"underscore-session"}},
		Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "socket-uuid"},
	}
	scope := antigravityReasoningReplayScopeFromRequest(context.Background(), "gemini-3.6-flash-high", cliproxyexecutor.Request{}, opts, nil)
	if got := scope.sessionKey; got != "responses:underscore-session" {
		t.Fatalf("session key = %q, want stable Responses session from Session_id header", got)
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

	if got := ojson.GetBytes(prepared2, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
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
	if got := ojson.GetBytes(resumed, "request.contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("resumed first signature = %q, want %q; body=%s", got, sig1, resumed)
	}
	if got := ojson.GetBytes(resumed, "request.contents.3.parts.0.thoughtSignature").String(); got != sig2 {
		t.Fatalf("resumed second signature = %q, want %q; body=%s", got, sig2, resumed)
	}
}

func TestAntigravityReasoningReplayIgnoresClaudeCodeAttributionSystemBlock(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model = "gemini-4-neon"
		sig1  = "claude-attribution-signature-one-123456"
	)
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"claude-attribution-session"}}

	turn1Original := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"first prompt"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321; cc_entrypoint=cli; cch=11111; cc_prompt_id=prompt-1;"},{"type":"text","text":"You are Claude Code."}],"thinking":{"type":"enabled"}}`)
	turn1Doc := ojson.NewDocString(`{"contents":[{"role":"user","parts":[{"text":"first prompt"}]}]}`)
	scope1, replayReq1, _, errPrepare1 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn1Original},
		cliproxyexecutor.Options{Headers: headers, OriginalRequest: turn1Original, SourceFormat: sdktranslator.FormatClaude},
		turn1Doc,
	)
	if errPrepare1 != nil {
		t.Fatal(errPrepare1)
	}
	accumulator1 := newAntigravityReasoningReplayAccumulator(scope1, replayReq1)
	accumulator1.observeResponsePayload([]byte(`{"candidates":[{"content":{"parts":[{"text":"first reply"},{"text":"","thoughtSignature":"` + sig1 + `"}]},"finishReason":"STOP"}]}`))
	accumulator1.Commit(context.Background())

	// Next prompt updates per-request cch and cc_prompt_id in the attribution system block.
	turn2Original := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"first prompt"}]},{"role":"assistant","content":[{"type":"text","text":"first reply"}]},{"role":"user","content":[{"type":"text","text":"second prompt"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321; cc_entrypoint=cli; cch=22222; cc_prompt_id=prompt-2;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"enabled"}}`)
	turn2Doc := ojson.NewDocString(`{"contents":[{"role":"user","parts":[{"text":"first prompt"}]},{"role":"model","parts":[{"text":"first reply"}]},{"role":"user","parts":[{"text":"second prompt"}]}]}`)
	scope2, _, changed2, errPrepare2 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn2Original},
		cliproxyexecutor.Options{Headers: headers, OriginalRequest: turn2Original, SourceFormat: sdktranslator.FormatClaude},
		turn2Doc,
	)
	if errPrepare2 != nil {
		t.Fatal(errPrepare2)
	}
	if scope2.sessionKey != scope1.sessionKey {
		t.Fatalf("replay scope changed across attribution block updates: turn1=%q turn2=%q", scope1.sessionKey, scope2.sessionKey)
	}
	if !changed2 {
		t.Fatalf("expected turn2 replay to restore cached text thoughtSignature, body=%s", turn2Doc.String())
	}
	if got := turn2Doc.Get("contents.1.parts.0.thoughtSignature").String(); got != sig1 {
		t.Fatalf("replayed text thoughtSignature = %q, want %q; body=%s", got, sig1, turn2Doc.String())
	}

	// A request without the attribution block must share the same lane as requests with it.
	strippedOriginal := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"first prompt"}]}],"system":[{"type":"text","text":"You are Claude Code."}]}`)
	strippedScope := antigravityReasoningReplayScopeFromRequest(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: strippedOriginal},
		cliproxyexecutor.Options{Headers: headers, OriginalRequest: strippedOriginal, SourceFormat: sdktranslator.FormatClaude},
		nil,
	)
	if strippedScope.sessionKey != scope1.sessionKey {
		t.Fatalf("scope without attribution block = %q, want %q", strippedScope.sessionKey, scope1.sessionKey)
	}

	// Auxiliary requests with different non-attribution system prompts must remain separated.
	titleOriginal := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"first prompt"}]}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321; cc_entrypoint=cli; cch=33333; cc_prompt_id=prompt-3;"},{"type":"text","text":"Generate a concise title that summarizes this session."}]}`)
	titleScope := antigravityReasoningReplayScopeFromRequest(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: titleOriginal},
		cliproxyexecutor.Options{Headers: headers, OriginalRequest: titleOriginal, SourceFormat: sdktranslator.FormatClaude},
		nil,
	)
	if titleScope.sessionKey == scope1.sessionKey {
		t.Fatalf("auxiliary title scope collided with main scope: %q", titleScope.sessionKey)
	}
}

func TestAntigravityClaudeReplaySystemLaneAttributionEdgeCases(t *testing.T) {
	stringOnly := []byte(`{"system":"x-anthropic-billing-header: cc_version=2.1.321; cch=11111;"}`)
	if lane := antigravityClaudeReplaySystemLane(stringOnly); lane != "" {
		t.Fatalf("attribution-only string system lane = %q, want empty", lane)
	}

	arrayOnly := []byte(`{"system":[{"type":"text","text":"  x-anthropic-billing-header: cc_version=2.1.321; cch=11111;"}]}`)
	if lane := antigravityClaudeReplaySystemLane(arrayOnly); lane != "" {
		t.Fatalf("attribution-only array system lane = %q, want empty", lane)
	}

	plainString := []byte(`{"system":"You are Claude Code."}`)
	if lane := antigravityClaudeReplaySystemLane(plainString); lane == "" {
		t.Fatal("non-attribution string system lane = empty, want non-empty hash")
	}

	withAttrA := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321; cch=aaaaa; cc_prompt_id=p1;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}]}`)
	withAttrB := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321; cch=bbbbb; cc_prompt_id=p2;"},{"type":"text","text":"You are Claude Code."}]}`)
	withoutAttr := []byte(`{"system":[{"type":"text","text":"You are Claude Code."}]}`)
	laneA := antigravityClaudeReplaySystemLane(withAttrA)
	laneB := antigravityClaudeReplaySystemLane(withAttrB)
	lanePlain := antigravityClaudeReplaySystemLane(withoutAttr)
	if laneA == "" || laneA != laneB || laneA != lanePlain {
		t.Fatalf("lanes diverged: withAttrA=%q withAttrB=%q withoutAttr=%q", laneA, laneB, lanePlain)
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
	wantKey := "session:" + generateStableSessionID(payload)
	if scope.sessionKey != wantKey {
		t.Fatalf("sessionKey = %q, want %q", scope.sessionKey, wantKey)
	}
	docScope := antigravityReasoningReplayScopeFromDoc("gemini-3-flash-agent", ojson.NewDoc(payload))
	if docScope.sessionKey != wantKey {
		t.Fatalf("doc sessionKey = %q, want %q", docScope.sessionKey, wantKey)
	}

	// Verify negative signed FNV-1a session IDs keep their leading minus sign in replay keys.
	negPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"a"}]}]}}`)
	negStable := generateStableSessionID(negPayload)
	if !strings.HasPrefix(negStable, "-") {
		t.Fatalf("expected negative signed FNV-1a ID for test fixture, got %q", negStable)
	}
	negScope := antigravityReasoningReplayScopeFromPayload("gemini-3-flash-agent", negPayload)
	if negScope.sessionKey != "session:"+negStable {
		t.Fatalf("negative payload sessionKey = %q, want %q", negScope.sessionKey, "session:"+negStable)
	}
	negDocScope := antigravityReasoningReplayScopeFromDoc("gemini-3-flash-agent", ojson.NewDoc(negPayload))
	if negDocScope.sessionKey != "session:"+negStable {
		t.Fatalf("negative doc sessionKey = %q, want %q", negDocScope.sessionKey, "session:"+negStable)
	}
}

func TestAntigravityReplayToolCallKeysUsesNativeFunctionCallID(t *testing.T) {
	fc := ojson.Parse(`{"name":"Read","args":{"file_path":"/a"},"id":"id-native"}`)
	keys := antigravityReplayToolCallKeysFromPart(fc)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	fc2 := ojson.Parse(`{"name":"Read","args":{"file_path":"/a"},"id":"id-native-2"}`)
	keys2 := antigravityReplayToolCallKeysFromPart(fc2)
	if keys[0] == keys2[0] {
		t.Fatalf("parallel tool calls should not share replay key: %v vs %v", keys, keys2)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRepairsSequentialCompactedUnknownResponseName(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	model := "gemini-3.6-flash-high"
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, []byte(`{"sessionId":"sess-seq-compact"}`)).sessionKey

	item := []byte(`{
		"type": "function_call_part",
		"contentIndex": 1,
		"partIndex": 0,
		"targetOccurrence": 0,
		"name": "Read",
		"call_id": "call_seq_1",
		"args": {"path": "/tmp/a"},
		"thoughtSignature": "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"
	}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("failed to cache replay item")
	}

	// Payload simulates Responses compaction where assistant function_call was dropped,
	// and translator generated functionResponse with placeholder name "unknown".
	compactedPayload := []byte(`{
		"sessionId": "sess-seq-compact",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Read file /tmp/a"}]
				},
				{
					"role": "user",
					"parts": [
						{
							"functionResponse": {
								"id": "call_seq_1",
								"name": "unknown",
								"response": {"output": "hello world"}
							}
						}
					]
				}
			]
		}
	}`)

	req := cliproxyexecutor.Request{Model: model, Payload: compactedPayload}
	opts := cliproxyexecutor.Options{}
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req, opts, compactedPayload)
	if errPrepare != nil {
		t.Fatalf("prepare failed unexpectedly: %v", errPrepare)
	}

	// Verify restored model functionCall has name "Read" and thoughtSignature
	fcName := ojson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String()
	fcID := ojson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String()
	fcSig := ojson.GetBytes(out, "request.contents.1.parts.0.thoughtSignature").String()
	if fcName != "Read" || fcID != "call_seq_1" || fcSig != "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg" {
		t.Fatalf("restored functionCall = name:%q id:%q sig:%q, want Read/call_seq_1/signature", fcName, fcID, fcSig)
	}

	// Verify user functionResponse.name was repaired from "unknown" to "Read"
	frName := ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	frID := ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	if frName != "Read" || frID != "call_seq_1" {
		t.Fatalf("repaired functionResponse = name:%q id:%q, want Read/call_seq_1", frName, frID)
	}

	// Verify ValidateGeminiFunctionCallPairing passes cleanly
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", errPairing)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRepairsParallelCompactedUnknownResponseName(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	model := "gemini-3.6-flash-high"
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, []byte(`{"sessionId":"sess-par-compact"}`)).sessionKey

	item1 := []byte(`{
		"type": "function_call_part",
		"contentIndex": 1,
		"partIndex": 0,
		"targetOccurrence": 0,
		"name": "Read",
		"call_id": "call_par_1",
		"args": {"path": "/tmp/a"},
		"thoughtSignature": "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"
	}`)
	item2 := []byte(`{
		"type": "function_call_part",
		"contentIndex": 1,
		"partIndex": 1,
		"targetOccurrence": 0,
		"name": "Grep",
		"call_id": "call_par_2",
		"args": {"query": "foo"},
		"thoughtSignature": "EvQCCvECARFNMg/sZy4s+7HU2/PDOR12"
	}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item1, item2}) {
		t.Fatal("failed to cache parallel replay items")
	}

	compactedPayload := []byte(`{
		"sessionId": "sess-par-compact",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Read /tmp/a and Grep foo"}]
				},
				{
					"role": "user",
					"parts": [
						{
							"functionResponse": {
								"id": "call_par_1",
								"name": "unknown",
								"response": {"output": "content a"}
							}
						},
						{
							"functionResponse": {
								"id": "call_par_2",
								"name": "unknown",
								"response": {"output": "matches b"}
							}
						}
					]
				}
			]
		}
	}`)

	req := cliproxyexecutor.Request{Model: model, Payload: compactedPayload}
	opts := cliproxyexecutor.Options{}
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req, opts, compactedPayload)
	if errPrepare != nil {
		t.Fatalf("prepare parallel failed: %v", errPrepare)
	}

	// Verify parallel functionCall restoration
	fc1Name := ojson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String()
	fc2Name := ojson.GetBytes(out, "request.contents.1.parts.1.functionCall.name").String()
	if fc1Name != "Read" || fc2Name != "Grep" {
		t.Fatalf("restored parallel functionCalls = %q, %q, want Read, Grep", fc1Name, fc2Name)
	}

	// Verify parallel functionResponse.name repair
	fr1Name := ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	fr2Name := ojson.GetBytes(out, "request.contents.2.parts.1.functionResponse.name").String()
	if fr1Name != "Read" || fr2Name != "Grep" {
		t.Fatalf("repaired parallel functionResponses = %q, %q, want Read, Grep", fr1Name, fr2Name)
	}

	// Verify strict pairing validation succeeds
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v", errPairing)
	}
}

func TestAntigravityFunctionCallMatchesReplayItemOnlyIgnoresDeclaredDefaults(t *testing.T) {
	item := ojson.Parse(`{"name":"Edit","args":{"path":"/tmp/a"}}`)
	schemas := map[string]any{"Edit": map[string]any{"type": "object", "properties": map[string]any{"replace_all": map[string]any{"type": "boolean", "default": false}}}}
	declaredDefault := ojson.Parse(`{"name":"Edit","args":{"path":"/tmp/a","replace_all":false}}`)
	if !antigravityFunctionCallMatchesReplayItem(declaredDefault, item, schemas) {
		t.Fatal("declared schema default should be semantically equivalent")
	}
	changedDefault := ojson.Parse(`{"name":"Edit","args":{"path":"/tmp/a","replace_all":true}}`)
	if antigravityFunctionCallMatchesReplayItem(changedDefault, item, schemas) {
		t.Fatal("non-default value must not match native args")
	}
	undeclaredExtra := ojson.Parse(`{"name":"Edit","args":{"path":"/tmp/a","force":false}}`)
	if antigravityFunctionCallMatchesReplayItem(undeclaredExtra, item, schemas) {
		t.Fatal("undeclared client arg must not match native args")
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresClaudeToolProvenanceWithSchemaDefault(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	const nativeID = "native-edit-1"
	const signature = "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"
	const nativeArgs = `{"file_path":"/tmp/a","old_string":"x","new_string":"y"}`
	clientID := util.GeminiClaudeToolUseID(nativeID, "Edit", nativeArgs)
	payload := []byte(`{"sessionId":"sess-claude-default","request":{"contents":[{"role":"user","parts":[{"text":"edit"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID + `","name":"Edit","args":{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":false}}}]},{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Edit","response":{"result":"ok"}}}]}]}}`)
	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"targetOccurrence":0,"name":"Edit","call_id":"` + nativeID + `","args":` + nativeArgs + `,"thoughtSignature":"` + signature + `"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("failed to cache native Edit provenance")
	}
	original := []byte(`{"model":"` + model + `","tools":[{"name":"Edit","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"},"replace_all":{"type":"boolean","default":false}}}}]}`)
	opts := cliproxyexecutor.Options{OriginalRequest: original, SourceFormat: sdktranslator.FromString("claude")}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	call := ojson.GetBytes(out, "request.contents.1.parts.0")
	if call.Get("functionCall.id").String() != nativeID || call.Get("functionCall.name").String() != "Edit" || call.Get("thoughtSignature").String() != signature {
		t.Fatalf("native call provenance was not restored: %s", call.Raw)
	}
	if call.Get("functionCall.args.replace_all").Exists() {
		t.Fatalf("client-inserted schema default leaked into restored native call: %s", call.Raw)
	}
	response := ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse")
	if response.Get("id").String() != nativeID || response.Get("name").String() != "Edit" {
		t.Fatalf("function response provenance was not restored: %s", response.Raw)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("restored history is invalid: %v", errPairing)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresLegacyClaudeToolIDWithSchemaDefault(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	payload := []byte(`{"sessionId":"sess-claude-legacy-default","request":{"contents":[{"role":"user","parts":[{"text":"edit"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"Edit-legacy-client-id","name":"Edit","args":{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":false}}}]},{"role":"user","parts":[{"functionResponse":{"id":"Edit-legacy-client-id","name":"Edit","response":{"result":"ok"}}}]}]}}`)
	item := []byte(`{"type":"function_call_part","contentIndex":1,"partIndex":0,"targetOccurrence":0,"name":"Edit","call_id":"native-edit-legacy","args":{"file_path":"/tmp/a","old_string":"x","new_string":"y"},"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item})
	original := []byte(`{"tools":[{"name":"Edit","input_schema":{"type":"object","properties":{"replace_all":{"type":"boolean","default":false}}}}]}`)
	opts := cliproxyexecutor.Options{OriginalRequest: original, SourceFormat: sdktranslator.FromString("claude")}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String(); got != "native-edit-legacy" {
		t.Fatalf("legacy client ID was not restored: %q", got)
	}
	if got := ojson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String(); got != "native-edit-legacy" {
		t.Fatalf("legacy functionResponse ID was not restored: %q", got)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayDegradesWithoutClaudeToolProvenance(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	clientID := util.GeminiClaudeToolUseID("native-missing", "Read", `{"file_path":"/tmp/a"}`)
	payload := []byte(`{"sessionId":"sess-missing-provenance","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID + `","name":"Read","args":{"file_path":"/tmp/a"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Read","response":{"result":"ok"}}}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}

	// An empty ledger must not kill the conversation: the reserved IDs are
	// rewritten to neutral synthetic IDs and the request stays valid.
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	if antigravityPayloadHasClaudeToolProvenanceID(out) {
		t.Fatalf("reserved provenance IDs leaked upstream: %s", out)
	}
	call := ojson.GetBytes(out, "request.contents.0.parts.0")
	response := ojson.GetBytes(out, "request.contents.1.parts.0.functionResponse")
	callID := call.Get("functionCall.id").String()
	if callID == "" || callID != response.Get("id").String() {
		t.Fatalf("degraded call/response pairing broken: call=%q response=%q", callID, response.Get("id").String())
	}
	if got := call.Get("thoughtSignature").String(); got != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first degraded call thoughtSignature = %q, want bypass sentinel", got)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("degraded history is invalid: %v", errPairing)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresParallelClaudeToolProvenance(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	const args1 = `{"file_path":"/tmp/a"}`
	const args2 = `{"file_path":"/tmp/b"}`
	clientID1 := util.GeminiClaudeToolUseID("native-read-1", "Read", args1)
	clientID2 := util.GeminiClaudeToolUseID("native-read-2", "Read", args2)
	payload := []byte(`{"sessionId":"sess-parallel-provenance","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID1 + `","name":"Read","args":{"file_path":"/tmp/a","offset":0}}},{"functionCall":{"id":"` + clientID2 + `","name":"Read","args":{"file_path":"/tmp/b","offset":0}}}]},{"role":"user","parts":[{"text":"results follow"},{"functionResponse":{"id":"` + clientID2 + `","name":"Read","response":{"result":"b"}}},{"functionResponse":{"id":"` + clientID1 + `","name":"Read","response":{"result":"a"}}},{"text":"continue"}]}]}}`)
	items := [][]byte{
		[]byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"name":"Read","call_id":"native-read-1","args":` + args1 + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`),
		[]byte(`{"type":"function_call_part","contentIndex":0,"partIndex":1,"targetOccurrence":0,"name":"Read","call_id":"native-read-2","args":` + args2 + `}`),
	}
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, items) {
		t.Fatal("failed to cache parallel provenance")
	}
	original := []byte(`{"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer","default":0}}}}]}`)
	opts := cliproxyexecutor.Options{OriginalRequest: original, SourceFormat: sdktranslator.FromString("claude")}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	calls := ojson.GetBytes(out, "request.contents.0.parts").Array()
	if len(calls) != 2 || calls[0].Get("functionCall.id").String() != "native-read-1" || calls[1].Get("functionCall.id").String() != "native-read-2" {
		t.Fatalf("parallel calls were not restored in native order: %s", out)
	}
	if calls[0].Get("thoughtSignature").String() == "" || calls[1].Get("thoughtSignature").Exists() {
		t.Fatalf("signed/unsigned parallel provenance changed: %s", ojson.GetBytes(out, "request.contents.0").Raw)
	}
	responses := ojson.GetBytes(out, "request.contents.1.parts").Array()
	if len(responses) != 4 || responses[0].Get("functionResponse.id").String() != "native-read-1" || responses[1].Get("functionResponse.id").String() != "native-read-2" {
		t.Fatalf("parallel responses were not normalized to native order: %s", ojson.GetBytes(out, "request.contents.1").Raw)
	}
	if responses[2].Get("text").String() != "results follow" || responses[3].Get("text").String() != "continue" {
		t.Fatalf("mixed user parts were not retained after parallel responses: %s", ojson.GetBytes(out, "request.contents.1").Raw)
	}
	if got := ojson.GetBytes(out, "request.contents.1.role").String(); got != "user" {
		t.Fatalf("mixed response role = %q, want user; output=%s", got, out)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("parallel restored history is invalid: %v", errPairing)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsChangedClaudeToolArguments(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	const nativeArgs = `{"file_path":"/tmp/a","old_string":"x","new_string":"y"}`
	clientID := util.GeminiClaudeToolUseID("native-edit-changed", "Edit", nativeArgs)
	payload := []byte(`{"sessionId":"sess-changed-args","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID + `","name":"Edit","args":{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":true}}}]},{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Edit","response":{"result":"ok"}}}]}]}}`)
	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"name":"Edit","call_id":"native-edit-changed","args":` + nativeArgs + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item})
	original := []byte(`{"tools":[{"name":"Edit","input_schema":{"type":"object","properties":{"replace_all":{"type":"boolean","default":false}}}}]}`)
	opts := cliproxyexecutor.Options{OriginalRequest: original, SourceFormat: sdktranslator.FromString("claude")}

	// The client changed the arguments, so the native call must NOT be restored.
	// The request still goes through, but only with a neutral synthetic ID and
	// without the native identity or the cached signature.
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	if antigravityPayloadHasClaudeToolProvenanceID(out) {
		t.Fatalf("reserved provenance IDs leaked upstream: %s", out)
	}
	call := ojson.GetBytes(out, "request.contents.0.parts.0")
	if got := call.Get("functionCall.id").String(); got == "native-edit-changed" {
		t.Fatalf("native call ID was restored onto changed arguments: %s", out)
	}
	if got := call.Get("thoughtSignature").String(); got != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("changed call thoughtSignature = %q, want bypass sentinel and no native signature", got)
	}
	if !call.Get("functionCall.args.replace_all").Bool() {
		t.Fatalf("client arguments were rewritten by replay: %s", call.Raw)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("degraded history is invalid: %v", errPairing)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRejectsUnmatchedNonPlaceholderResponseName(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	model := "gemini-3.6-flash-high"
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, []byte(`{"sessionId":"sess-mismatch"}`)).sessionKey

	item := []byte(`{
		"type": "function_call_part",
		"contentIndex": 1,
		"partIndex": 0,
		"targetOccurrence": 0,
		"name": "Read",
		"call_id": "call_mismatch_1",
		"args": {"path": "/tmp/a"},
		"thoughtSignature": "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"
	}`)
	internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item})

	// Payload has a non-placeholder name mismatch ("Write" != "Read")
	mismatchedPayload := []byte(`{
		"sessionId": "sess-mismatch",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Do task"}]
				},
				{
					"role": "user",
					"parts": [
						{
							"functionResponse": {
								"id": "call_mismatch_1",
								"name": "Write",
								"response": {"output": "ok"}
							}
						}
					]
				}
			]
		}
	}`)

	req := cliproxyexecutor.Request{Model: model, Payload: mismatchedPayload}
	opts := cliproxyexecutor.Options{}
	_, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req, opts, mismatchedPayload)
	if errPrepare == nil {
		t.Fatal("expected 400 error for non-placeholder name mismatch, got nil")
	}
	if !strings.Contains(errPrepare.Error(), "invalid Gemini function call history") {
		t.Fatalf("error = %v, want invalid Gemini function call history", errPrepare)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayRestoresIdentityOnContextDrift(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	const args = `{"file_path":"/tmp/a"}`
	clientID := util.GeminiClaudeToolUseID("native-drift", "Read", args)
	payload := []byte(`{"sessionId":"sess-context-drift","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"id":"` + clientID + `","name":"Read","args":` + args + `}}]},{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Read","response":{"result":"ok"}}}]}]}}`)
	// A stale contextHash stands in for compacted or rewritten history: the tool
	// identity is still provable from the opaque ID, but the cached signature is
	// no longer valid for this conversation.
	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"name":"Read","call_id":"native-drift","args":` + args + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg","contextHash":"0000000000000000000000000000000000000000000000000000000000000000"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("failed to cache drifted provenance")
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepare failed: %v", errPrepare)
	}
	if antigravityPayloadHasClaudeToolProvenanceID(out) {
		t.Fatalf("reserved provenance IDs leaked upstream: %s", out)
	}
	call := ojson.GetBytes(out, "request.contents.0.parts.0")
	if got := call.Get("functionCall.id").String(); got != "native-drift" {
		t.Fatalf("functionCall.id = %q, want native identity restored despite context drift", got)
	}
	if got := ojson.GetBytes(out, "request.contents.1.parts.0.functionResponse.id").String(); got != "native-drift" {
		t.Fatalf("functionResponse.id = %q, want native identity restored", got)
	}
	if got := call.Get("thoughtSignature").String(); !antigravityHasNativeThoughtSignature(got) {
		t.Fatalf("thoughtSignature = %q, want the native signature replayed even though the context drifted", got)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("restored history is invalid: %v", errPairing)
	}
}

func TestDegradeAntigravityClaudeToolProvenanceIDsKeepsParallelShape(t *testing.T) {
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = util.GeminiClaudeToolUseID(fmt.Sprintf("native-%d", i), "Read", `{"file_path":"/tmp/a"}`)
	}
	payload := []byte(`{"request":{"contents":[{"role":"model","parts":[` +
		`{"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg","functionCall":{"id":"` + ids[0] + `","name":"Read","args":{"file_path":"/tmp/a"}}},` +
		`{"functionCall":{"id":"` + ids[1] + `","name":"Read","args":{"file_path":"/tmp/b"}}},` +
		`{"functionCall":{"id":"` + ids[2] + `","name":"Read","args":{"file_path":"/tmp/c"}}}` +
		`]},{"role":"user","parts":[` +
		`{"functionResponse":{"id":"` + ids[0] + `","name":"Read","response":{"result":"a"}}},` +
		`{"functionResponse":{"id":"` + ids[1] + `","name":"Read","response":{"result":"b"}}},` +
		`{"functionResponse":{"id":"` + ids[2] + `","name":"Read","response":{"result":"c"}}}` +
		`]}]}}`)

	out, degraded := degradeAntigravityClaudeToolProvenanceIDs(payload)
	out = antigravityRepairUnsignedFirstFunctionCalls(out)
	if degraded != 6 {
		t.Fatalf("degraded = %d, want 6 (3 calls + 3 responses)", degraded)
	}
	if antigravityPayloadHasClaudeToolProvenanceID(out) {
		t.Fatalf("reserved provenance IDs leaked upstream: %s", out)
	}

	calls := ojson.GetBytes(out, "request.contents.0.parts").Array()
	responses := ojson.GetBytes(out, "request.contents.1.parts").Array()
	if len(calls) != 3 || len(responses) != 3 {
		t.Fatalf("part counts changed: %d calls, %d responses", len(calls), len(responses))
	}
	signed := 0
	for i, call := range calls {
		signature := call.Get("thoughtSignature").String()
		if signature != "" {
			signed++
		}
		if i == 0 && signature != internalsignature.GeminiSkipThoughtSignatureValidator {
			t.Fatalf("first call thoughtSignature = %q, want bypass sentinel signature", signature)
		}
		if i > 0 && signature != "" {
			t.Fatalf("sibling call %d gained a signature %q, want unsigned", i, signature)
		}
		if got := call.Get("functionCall.id").String(); got != responses[i].Get("functionResponse.id").String() {
			t.Fatalf("call/response pairing broken at %d: %q vs %q", i, got, responses[i].Get("functionResponse.id").String())
		}
	}
	if signed != 1 {
		t.Fatalf("signed calls = %d, want exactly 1 signed + 2 unsigned native parallel shape", signed)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("degraded history is invalid: %v", errPairing)
	}
}

func TestDegradeAntigravityClaudeToolProvenanceIDs_AllCallsSigned_OnlyFirstGetsBypassSentinel(t *testing.T) {
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = util.GeminiClaudeToolUseID(fmt.Sprintf("native-signed-%d", i), "Read", `{"file_path":"/tmp/a"}`)
	}
	payload := []byte(`{"request":{"contents":[{"role":"model","parts":[` +
		`{"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg","functionCall":{"id":"` + ids[0] + `","name":"Read","args":{"file_path":"/tmp/a"}}},` +
		`{"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xh","functionCall":{"id":"` + ids[1] + `","name":"Read","args":{"file_path":"/tmp/b"}}},` +
		`{"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xi","functionCall":{"id":"` + ids[2] + `","name":"Read","args":{"file_path":"/tmp/c"}}}` +
		`]},{"role":"user","parts":[` +
		`{"functionResponse":{"id":"` + ids[0] + `","name":"Read","response":{"result":"a"}}},` +
		`{"functionResponse":{"id":"` + ids[1] + `","name":"Read","response":{"result":"b"}}},` +
		`{"functionResponse":{"id":"` + ids[2] + `","name":"Read","response":{"result":"c"}}}` +
		`]}]}}`)

	out, degraded := degradeAntigravityClaudeToolProvenanceIDs(payload)
	out = antigravityRepairUnsignedFirstFunctionCalls(out)
	if degraded != 6 {
		t.Fatalf("degraded = %d, want 6", degraded)
	}

	calls := ojson.GetBytes(out, "request.contents.0.parts").Array()
	if len(calls) != 3 {
		t.Fatalf("call count: %d", len(calls))
	}
	if calls[0].Get("thoughtSignature").String() != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first call thoughtSignature = %q, want bypass sentinel", calls[0].Get("thoughtSignature").String())
	}
	if calls[1].Get("thoughtSignature").Exists() && calls[1].Get("thoughtSignature").String() != "" {
		t.Fatalf("second call thoughtSignature = %q, want deleted/empty", calls[1].Get("thoughtSignature").String())
	}
	if calls[2].Get("thoughtSignature").Exists() && calls[2].Get("thoughtSignature").String() != "" {
		t.Fatalf("third call thoughtSignature = %q, want deleted/empty", calls[2].Get("thoughtSignature").String())
	}

	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("pairing invalid: %v", errPairing)
	}
	if errSig := internalsignature.ValidateGeminiThoughtSignatures(out, internalsignature.GeminiThoughtSignatureValidationOptions{AllowBypassSentinel: true}); errSig != nil {
		t.Fatalf("ValidateGeminiThoughtSignatures failed: %v\noutput: %s", errSig, out)
	}
}

func TestDegradeAntigravityClaudeToolProvenanceIDs_StrictMonotonicOffsets(t *testing.T) {
	id := util.GeminiClaudeToolUseID("native-1", "Read", `{"file_path":"/tmp/a"}`)
	payload := []byte(fmt.Sprintf(`{"request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"sig-1","functionCall":{"id":"%s","name":"Read","args":{"file_path":"/tmp/a"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"%s","name":"Read","response":{"result":"ok"}}}]}]}}`, id, id))

	out, degraded := degradeAntigravityClaudeToolProvenanceIDs(payload)
	if degraded != 2 {
		t.Fatalf("degraded = %d, want 2", degraded)
	}
	if bytes.Equal(out, payload) {
		t.Fatal("expected payload to be modified")
	}
	if strings.Contains(string(out), id) {
		t.Fatalf("expected reserved ID %s to be replaced", id)
	}

	syntheticID := antigravitySyntheticToolCallID(id)
	if !strings.Contains(string(out), syntheticID) {
		t.Fatalf("expected synthetic ID %s in output, got: %s", syntheticID, string(out))
	}
}

var antigravityDegradeBenchmarkOutput []byte

func BenchmarkDegradeAntigravityClaudeToolProvenanceIDs(b *testing.B) {
	const calls = 217
	var payload strings.Builder
	payload.Grow(1<<20 + calls*512)
	payload.WriteString(`{"request":{"contents":[{"role":"user","parts":[{"text":"`)
	payload.WriteString(strings.Repeat("x", 1<<20))
	payload.WriteString(`"}]}`)
	for i := range calls {
		id := util.GeminiClaudeToolUseID(fmt.Sprintf("native-%d", i), "Read", `{"file_path":"/tmp/a"}`)
		fmt.Fprintf(&payload, `,{"role":"model","parts":[{"thoughtSignature":"signature-%d","functionCall":{"id":"%s","name":"Read","args":{"file_path":"/tmp/a"}}}]}`, i, id)
		fmt.Fprintf(&payload, `,{"role":"user","parts":[{"functionResponse":{"id":"%s","name":"Read","response":{"result":"ok"}}}]}`, id)
	}
	payload.WriteString(`]}}`)
	input := []byte(payload.String())
	if _, degraded := degradeAntigravityClaudeToolProvenanceIDs(input); degraded != calls*2 {
		b.Fatalf("degraded = %d, want %d", degraded, calls*2)
	}

	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		var degraded int
		antigravityDegradeBenchmarkOutput, degraded = degradeAntigravityClaudeToolProvenanceIDs(input)
		if degraded != calls*2 {
			b.Fatalf("degraded = %d, want %d", degraded, calls*2)
		}
	}
}

func TestPrepareAntigravityGeminiReasoningReplay_ReordersPermutedParallelToolResponsesWithDegradedIDs(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.7-flash-high"
	const count = 12
	calls := make([]string, count)
	responses := make([]string, count)
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		ids[i] = util.GeminiClaudeToolUseID(fmt.Sprintf("native-%d", i), "Read", `{"file_path":"/tmp/a"}`)
		sig := ""
		if i == 0 {
			sig = `, "thoughtSignature": "skip_thought_signature_validator"`
		}
		calls[i] = fmt.Sprintf(`{"functionCall":{"id":"%s","name":"Read","args":{"file_path":"/tmp/a"}}%s}`, ids[i], sig)
	}

	// Permute responses: reverse order
	for i := 0; i < count; i++ {
		permutedIndex := count - 1 - i
		responses[i] = fmt.Sprintf(`{"functionResponse":{"id":"%s","name":"Read","response":{"result":"ok-%d"}}}`, ids[permutedIndex], permutedIndex)
	}

	payload := []byte(fmt.Sprintf(`{"sessionId":"sess-permuted","request":{"contents":[{"role":"model","parts":[%s]},{"role":"user","parts":[%s]}]}}`,
		strings.Join(calls, ","),
		strings.Join(responses, ","),
	))

	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}
	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepareAntigravityGeminiReasoningReplayPayload error: %v", errPrepare)
	}

	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v\noutput: %s", errPairing, out)
	}

	outCalls := ojson.GetBytes(out, "request.contents.0.parts").Array()
	outResponses := ojson.GetBytes(out, "request.contents.1.parts").Array()
	if len(outCalls) != count || len(outResponses) != count {
		t.Fatalf("part counts: %d calls, %d responses", len(outCalls), len(outResponses))
	}
	for i := 0; i < count; i++ {
		callID := outCalls[i].Get("functionCall.id").String()
		respID := outResponses[i].Get("functionResponse.id").String()
		if callID == "" || respID == "" || callID != respID {
			t.Fatalf("part %d id mismatch: call %q != resp %q", i, callID, respID)
		}
	}
}

func TestPrepareAntigravityGeminiReasoningReplayStillRejectsBrokenPairing(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	clientID := util.GeminiClaudeToolUseID("native-orphan", "Read", `{"file_path":"/tmp/a"}`)
	// A functionResponse with no preceding functionCall is structurally invalid and
	// must keep failing even though provenance degradation is now in play.
	payload := []byte(`{"sessionId":"sess-orphan","request":{"contents":[{"role":"user","parts":[{"functionResponse":{"id":"` + clientID + `","name":"Read","response":{"result":"ok"}}}]}]}}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")}

	_, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare == nil || !strings.Contains(errPrepare.Error(), "invalid Gemini function call history") {
		t.Fatalf("error = %v, want structural pairing rejection", errPrepare)
	}
}

func TestAntigravityReasoningReplaySkipsSymmetricGeminiAndInteractionsButKeepsAsymmetricAlias(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3-flash"
	payload := []byte(`{"sessionId":"sess-symmetry","request":{"contents":[{"role":"user","parts":[{"text":"Call get_weather for Beijing"}]}]}}`)
	responseSSE := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":""},{"thoughtSignature":"EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX","functionCall":{"name":"get_weather","args":{"city":"Beijing"},"id":"call_1"}}]},"finishReason":"STOP"}]}}`)

	for _, format := range []sdktranslator.Format{sdktranslator.FormatGemini, sdktranslator.FormatInteractions} {
		t.Run("symmetric_"+format.String(), func(t *testing.T) {
			internalcache.ClearAntigravityReasoningReplayCache()
			req := cliproxyexecutor.Request{Model: model, Payload: payload}
			opts := cliproxyexecutor.Options{
				SourceFormat:   format,
				ResponseFormat: format,
			}
			_, scope, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req, opts, payload)
			if errPrepare != nil {
				t.Fatalf("prepare error: %v", errPrepare)
			}
			if scope.valid() {
				t.Fatalf("symmetric format %q must not allocate reasoning replay KV scope, got %+v", format, scope)
			}
			if acc := newAntigravityReasoningReplayAccumulator(scope, payload); acc != nil {
				t.Fatalf("symmetric format %q must not create replay accumulator", format)
			}
		})
	}

	t.Run("asymmetric_claude_haiku_alias_to_gemini_3_flash", func(t *testing.T) {
		internalcache.ClearAntigravityReasoningReplayCache()
		// Downstream client sends Claude /v1/messages with model="claude-haiku-4-5-20251001",
		// which model-alias resolves to baseModel="gemini-3-flash" inside AntigravityExecutor.
		req := cliproxyexecutor.Request{Model: model, Payload: payload}
		opts := cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
			Metadata: map[string]any{
				cliproxyexecutor.RequestedModelMetadataKey: "claude-haiku-4-5-20251001",
			},
		}
		_, scope, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req, opts, payload)
		if errPrepare != nil {
			t.Fatalf("prepare error: %v", errPrepare)
		}
		if !scope.valid() {
			t.Fatal("asymmetric Claude -> Gemini alias request must allocate reasoning replay KV scope")
		}
		acc := newAntigravityReasoningReplayAccumulator(scope, payload)
		if acc == nil {
			t.Fatal("expected non-nil accumulator for asymmetric Claude -> Gemini request")
		}
		acc.ObserveSSELine(responseSSE)
		acc.Commit(context.Background())

		items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey)
		if !ok || len(items) == 0 {
			t.Fatalf("expected asymmetric replay items to be persisted in KV, ok=%v len=%d", ok, len(items))
		}
	})

	t.Run("top_level_gemini_doc_replays_asymmetric_and_skips_symmetric", func(t *testing.T) {
		internalcache.ClearAntigravityReasoningReplayCache()
		topLevelTurn1 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Call get_weather for Beijing"}]}]}`)
		topLevelTurn2 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Call get_weather for Beijing"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"get_weather","args":{"city":"Beijing"},"id":"call_1"}}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_1","response":{"result":"sunny"}}}]}]}`)
		topLevelResponse := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":""},{"thoughtSignature":"EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX","functionCall":{"name":"get_weather","args":{"city":"Beijing"},"id":"call_1"}}]},"finishReason":"STOP"}]}`)

		symDoc := ojson.NewDoc(topLevelTurn1)
		symScope, _, _, errSym := prepareGeminiTopLevelReasoningReplayPayloadDoc(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: topLevelTurn1},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini},
			symDoc,
		)
		if errSym != nil {
			t.Fatalf("symmetric top-level prepare error: %v", errSym)
		}
		if symScope.valid() {
			t.Fatalf("symmetric top-level Gemini must not allocate replay scope, got %+v", symScope)
		}

		headers := make(http.Header)
		headers.Set("Session-Id", "sess-non-ag-gemini")
		asymOpts := cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAIResponse,
			ResponseFormat: sdktranslator.FormatOpenAIResponse,
			Headers:        headers,
		}
		doc1 := ojson.NewDoc(topLevelTurn1)
		scope1, replayReq1, _, err1 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: topLevelTurn1},
			asymOpts,
			doc1,
		)
		if err1 != nil || !scope1.valid() {
			t.Fatalf("asymmetric top-level prepare turn 1: scope=%+v err=%v", scope1, err1)
		}
		cacheAntigravityReasoningReplayFromResponse(context.Background(), scope1, replayReq1, topLevelResponse)

		doc2 := ojson.NewDoc(topLevelTurn2)
		scope2, replayReq2, changed2, err2 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: topLevelTurn2},
			asymOpts,
			doc2,
		)
		if err2 != nil || !changed2 {
			t.Fatalf("asymmetric top-level prepare turn 2: changed=%v err=%v", changed2, err2)
		}
		if gotSig := doc2.Get("contents.1.parts.0.thoughtSignature").String(); gotSig != "EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX" {
			t.Fatalf("replayed signature = %q, want native thoughtSignature", gotSig)
		}
		if gotRole := doc2.Get("contents.2.role").String(); gotRole != "user" {
			t.Fatalf("non-Antigravity functionResponse role = %q, want user", gotRole)
		}

		// Turn 2 simulates Vertex stripping wire-only functionCall.id/functionResponse.id
		// after prepareGeminiTopLevelReasoningReplayPayloadDoc; the captured replayRequestPayload
		// from prepareGeminiTopLevelReasoningReplayPayloadDoc must still keep canonical call_id
		// context so Turn 3 matches both call_1 and call_2 signatures.
		helps.StripVertexOpenAIResponsesToolCallIDsDoc(doc2, asymOpts.SourceFormat.String())
		topLevelResponse2 := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":""},{"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg","functionCall":{"name":"get_weather","args":{"city":"Shanghai"},"id":"call_2"}}]},"finishReason":"STOP"}]}`)
		cacheAntigravityReasoningReplayFromResponse(context.Background(), scope2, replayReq2, topLevelResponse2)

		topLevelTurn3 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Call get_weather for Beijing"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"get_weather","args":{"city":"Beijing"},"id":"call_1"}}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_1","response":{"result":"sunny"}}}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"get_weather","args":{"city":"Shanghai"},"id":"call_2"}}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_2","response":{"result":"cloudy"}}}]}]}`)
		doc3 := ojson.NewDoc(topLevelTurn3)
		_, _, changed3, err3 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: topLevelTurn3},
			asymOpts,
			doc3,
		)
		if err3 != nil || !changed3 {
			t.Fatalf("asymmetric top-level prepare turn 3: changed=%v err=%v", changed3, err3)
		}
		if gotSig1 := doc3.Get("contents.1.parts.0.thoughtSignature").String(); gotSig1 != "EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX" {
			t.Fatalf("turn 3 call_1 signature = %q, want preserved signature", gotSig1)
		}
		if gotSig2 := doc3.Get("contents.3.parts.0.thoughtSignature").String(); gotSig2 != "EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg" {
			t.Fatalf("turn 3 call_2 signature = %q, want second signature", gotSig2)
		}
	})
}

func TestPrepareAntigravityGeminiReasoningReplayDegradesWhenReplayBreaksPairing(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.6-flash-high"
	const args = `{"file_path":"/tmp/a"}`
	payload := []byte(`{"sessionId":"sess-pairing-break","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"Read","args":` + args + `}}]},{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"result":"ok"}}}]}]}}`)
	payload = normalizeAntigravityGeminiFunctionResponseRoles(payload)
	if err := internalsignature.ValidateGeminiFunctionCallPairing(payload); err != nil {
		t.Fatalf("original payload pairing invalid: %v", err)
	}

	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"name":"Write","args":` + args + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{item}) {
		t.Fatal("failed to cache replay item")
	}
	opts := cliproxyexecutor.Options{}

	out, _, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, opts, payload)
	if errPrepare != nil {
		t.Fatalf("prepareAntigravityGeminiReasoningReplayPayload error: %v, want graceful degradation to original payload", errPrepare)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("out = %s, want original payload %s", out, payload)
	}
}

func TestPrepareAntigravityGeminiReasoningReplayDegradesAndCommitsNextTurn(t *testing.T) {
	modes := []struct {
		name  string
		useKV bool
	}{
		{name: "memory", useKV: false},
		{name: "kv", useKV: true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			homekv.ClearCurrent()
			internalcache.ClearAntigravityReasoningReplayCache()
			if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
				t.Fatalf("kvstore.ShutdownLocal() setup error = %v", errShutdown)
			}
			var dbPath string
			if mode.useKV {
				// Allocate TempDir before registering ShutdownLocal cleanup so LIFO
				// order closes bbolt before removing the directory (required on Windows).
				dbPath = filepath.Join(t.TempDir(), "issue7997-kv.db")
			}
			t.Cleanup(func() {
				internalcache.ClearAntigravityReasoningReplayCache()
				homekv.ClearCurrent()
				if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
					t.Errorf("kvstore.ShutdownLocal() cleanup error = %v", errShutdown)
				}
			})
			if mode.useKV {
				if errCfg := kvstore.ConfigureLocal(true, dbPath); errCfg != nil {
					t.Fatalf("kvstore.ConfigureLocal() error = %v", errCfg)
				}
			}

			const model = "gemini-3.6-flash-high"
			const args = `{"file_path":"/tmp/a"}`
			payload := []byte(`{"sessionId":"sess-pairing-break-commit-` + mode.name + `","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"Read","args":` + args + `}}]},{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"result":"ok"}}}]}]}}`)
			payload = normalizeAntigravityGeminiFunctionResponseRoles(payload)
			if err := internalsignature.ValidateGeminiFunctionCallPairing(payload); err != nil {
				t.Fatalf("original payload pairing invalid: %v", err)
			}

			badItem := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"call_id":"call_native_1","name":"Read","args":` + args + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`)
			sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
			if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{badItem}) {
				t.Fatal("failed to cache bad replay item")
			}

			opts := cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FromString("claude"),
				OriginalRequest: []byte(`{"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}`),
			}
			out, scope, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(
				context.Background(),
				model,
				cliproxyexecutor.Request{Model: model, Payload: payload},
				opts,
				payload,
			)
			if errPrepare != nil {
				t.Fatalf("prepareAntigravityGeminiReasoningReplayPayload error: %v, want graceful degradation", errPrepare)
			}
			if !bytes.Equal(out, payload) {
				t.Fatalf("out = %s, want original payload %s", out, payload)
			}
			if !scope.valid() {
				t.Fatalf("returned scope is invalid: %+v", scope)
			}

			_, foundAfterDegrade, errPeekAfterDegrade := internalcache.PeekAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey)
			if errPeekAfterDegrade != nil || foundAfterDegrade {
				t.Fatalf("cache after pairing degradation = (found=%v, err=%v), want (false, nil)", foundAfterDegrade, errPeekAfterDegrade)
			}

			const newSig = "FreshSignatureAfterPairingDegradation1234567890"
			responseBody := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"checking next step"},{"thoughtSignature":"` + newSig + `","functionCall":{"name":"Grep","args":{"pattern":"foo"}}}]},"finishReason":"STOP"}]}}`)
			cacheAntigravityReasoningReplayFromResponse(context.Background(), scope, out, responseBody)

			committedItems, foundAfterCommit, errPeekAfterCommit := internalcache.PeekAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey)
			if errPeekAfterCommit != nil || !foundAfterCommit || len(committedItems) == 0 {
				t.Fatalf("cache after degraded turn commit = (len=%d, found=%v, err=%v), want committed replay items", len(committedItems), foundAfterCommit, errPeekAfterCommit)
			}
			hasNewSig := false
			for _, item := range committedItems {
				if ojson.GetBytesNoCopy(item, "thoughtSignature").String() == newSig {
					hasNewSig = true
					break
				}
			}
			if !hasNewSig {
				t.Fatalf("committed items missing %q: %q", newSig, committedItems)
			}

			nextTurnPayload := []byte(`{"sessionId":"sess-pairing-break-commit-` + mode.name + `","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"Read","args":` + args + `}}]},{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"result":"ok"}}}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"Grep","args":{"pattern":"foo"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"Grep","response":{"result":"match"}}}]}]}}`)
			nextTurnPayload = normalizeAntigravityGeminiFunctionResponseRoles(nextTurnPayload)
			replayedNext, _, errNext := prepareAntigravityGeminiReasoningReplayPayload(
				context.Background(),
				model,
				cliproxyexecutor.Request{Model: model, Payload: nextTurnPayload},
				opts,
				nextTurnPayload,
			)
			if errNext != nil {
				t.Fatalf("next turn prepare error: %v", errNext)
			}
			if gotSig := ojson.GetBytesNoCopy(replayedNext, "request.contents.2.parts.0.thoughtSignature").String(); gotSig != newSig {
				t.Fatalf("next turn replayed signature = %q, want %q", gotSig, newSig)
			}
		})
	}
}

func TestPrepareAntigravityGeminiReasoningReplayDegradedCommitDoesNotClobberConcurrentWrite(t *testing.T) {
	homekv.ClearCurrent()
	internalcache.ClearAntigravityReasoningReplayCache()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatalf("kvstore.ShutdownLocal() setup error = %v", errShutdown)
	}
	t.Cleanup(func() {
		internalcache.ClearAntigravityReasoningReplayCache()
		homekv.ClearCurrent()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Errorf("kvstore.ShutdownLocal() cleanup error = %v", errShutdown)
		}
	})

	const model = "gemini-3.6-flash-high"
	const args = `{"file_path":"/tmp/a"}`
	payload := []byte(`{"sessionId":"sess-pairing-break-concurrent","request":{"contents":[{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"Read","args":` + args + `}}]},{"role":"user","parts":[{"functionResponse":{"name":"Read","response":{"result":"ok"}}}]}]}}`)
	payload = normalizeAntigravityGeminiFunctionResponseRoles(payload)

	badItem := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"call_id":"call_native_1","name":"Read","args":` + args + `,"thoughtSignature":"EsMTCsATARFNMg/XNVix5lDpkKaHR7Xg"}`)
	sessionKey := antigravityReasoningReplayScopeFromPayload(model, payload).sessionKey
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{badItem}) {
		t.Fatal("failed to cache bad replay item")
	}

	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: []byte(`{"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]}`),
	}
	out, scope, errPrepare := prepareAntigravityGeminiReasoningReplayPayload(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: payload},
		opts,
		payload,
	)
	if errPrepare != nil {
		t.Fatalf("prepareAntigravityGeminiReasoningReplayPayload error: %v", errPrepare)
	}

	const concurrentSig = "ConcurrentTurnSignatureMustSurvive1234567890"
	concurrentItem := []byte(`{"type":"thought_signature","contentIndex":0,"partIndex":0,"thoughtSignature":"` + concurrentSig + `"}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, sessionKey, [][]byte{concurrentItem}) {
		t.Fatal("failed to cache concurrent replay item")
	}

	responseBody := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"DegradedStaleSignatureShouldDrop1234567890","functionCall":{"name":"Grep","args":{"pattern":"foo"}}}]},"finishReason":"STOP"}]}}`)
	cacheAntigravityReasoningReplayFromResponse(context.Background(), scope, out, responseBody)

	items, found, errPeek := internalcache.PeekAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey)
	if errPeek != nil || !found || len(items) != 1 || ojson.GetBytesNoCopy(items[0], "thoughtSignature").String() != concurrentSig {
		t.Fatalf("concurrent replay item was clobbered: items=%q found=%v err=%v", items, found, errPeek)
	}
}

func TestAntigravityStreamChunksHaveTerminalCommitEvent(t *testing.T) {
	cases := []struct {
		name   string
		chunks [][]byte
		want   bool
	}{
		{
			name:   "openai responses completed",
			chunks: [][]byte{[]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")},
			want:   true,
		},
		{
			name:   "openai responses incomplete",
			chunks: [][]byte{[]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")},
			want:   true,
		},
		{
			name:   "openai chat finish_reason stop",
			chunks: [][]byte{[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")},
			want:   true,
		},
		{
			name:   "openai chat null finish_reason",
			chunks: [][]byte{[]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")},
			want:   false,
		},
		{
			name:   "claude message_stop",
			chunks: [][]byte{[]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")},
			want:   true,
		},
		{
			name:   "bare done frame only",
			chunks: [][]byte{[]byte("data: [DONE]\n\n")},
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityStreamChunksHaveTerminalCommitEvent(tc.chunks); got != tc.want {
				t.Fatalf("antigravityStreamChunksHaveTerminalCommitEvent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAntigravityReasoningReplayAccumulatorAssemblesVertexStreamingPartialArgs(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.8-flash"
	asymOpts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Headers:        http.Header{"X-Session-Id": []string{"sess-vertex-partial-args"}},
	}

	turn1 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Weather in New York?"}]}]}`)
	doc1 := ojson.NewDoc(turn1)
	scope1, replayReq1, _, err1 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn1},
		asymOpts,
		doc1,
	)
	if err1 != nil || !scope1.valid() {
		t.Fatalf("prepare turn 1: scope=%+v err=%v", scope1, err1)
	}

	acc := newAntigravityReasoningReplayAccumulator(scope1, replayReq1)
	acc.ObserveSSELine([]byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_ny","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}]}`))
	acc.ObserveSSELine([]byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"","thoughtSignature":"EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX"}]}}]}`))
	acc.ObserveSSELine([]byte(`data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.city","stringValue":"York"}],"willContinue":false}}]},"finishReason":"STOP"}]}`))
	acc.Commit(context.Background())

	turn2 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Weather in New York?"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"get_weather","args":{"city":"New York"},"id":"call_ny"}}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_ny","response":{"result":"sunny"}}}]}]}`)
	doc2 := ojson.NewDoc(turn2)
	_, _, changed2, err2 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn2},
		asymOpts,
		doc2,
	)
	if err2 != nil || !changed2 {
		t.Fatalf("prepare turn 2 after partialArgs stream: changed=%v err=%v", changed2, err2)
	}
	if gotSig := doc2.Get("contents.1.parts.0.thoughtSignature").String(); gotSig != "EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX" {
		t.Fatalf("replayed signature = %q, want native signature from partialArgs stream", gotSig)
	}
}

func TestNonVertexSanitizesReplayedVertexBareTinkSignature(t *testing.T) {
	// Verify that a Vertex bare-Tink thoughtSignature replayed from shared KV is
	// downgraded to skip_thought_signature_validator when post-replay non-Vertex
	// sanitization runs (as in GeminiExecutor and AIStudioExecutor).
	const vertexBareTinkSig = "AY89a18f0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	internalcache.ClearAntigravityReasoningReplayCache()
	const model = "gemini-3.8-flash"
	asymOpts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Headers:        http.Header{"X-Session-Id": []string{"sess-vertex-to-gemini"}},
	}
	turn1 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Call get_weather"}]}]}`)
	doc1 := ojson.NewDoc(turn1)
	scope1, replayReq1, _, err1 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn1},
		asymOpts,
		doc1,
	)
	if err1 != nil || !scope1.valid() {
		t.Fatalf("prepare turn 1: scope=%+v err=%v", scope1, err1)
	}
	vertexResp := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + vertexBareTinkSig + `","functionCall":{"name":"get_weather","args":{"city":"Tokyo"},"id":"call_tink"}}]},"finishReason":"STOP"}]}`)
	cacheAntigravityReasoningReplayFromResponse(context.Background(), scope1, replayReq1, vertexResp)

	turn2 := []byte(`{"contents":[{"role":"user","parts":[{"text":"Call get_weather"}]},{"role":"model","parts":[{"thoughtSignature":"skip_thought_signature_validator","functionCall":{"name":"get_weather","args":{"city":"Tokyo"},"id":"call_tink"}}]},{"role":"user","parts":[{"functionResponse":{"name":"get_weather","id":"call_tink","response":{"result":"clear"}}}]}]}`)
	nonVertexDoc := ojson.NewDoc(turn2)
	_, _, changed2, err2 := prepareGeminiTopLevelReasoningReplayPayloadDoc(
		context.Background(),
		model,
		cliproxyexecutor.Request{Model: model, Payload: turn2},
		asymOpts,
		nonVertexDoc,
	)
	if err2 != nil || !changed2 {
		t.Fatalf("prepare turn 2: changed=%v err=%v", changed2, err2)
	}
	internalsignature.SanitizeNonVertexGeminiRequestThoughtSignaturesDoc(nonVertexDoc, "contents")
	if got := nonVertexDoc.Get("contents.1.parts.0.thoughtSignature").String(); got != "skip_thought_signature_validator" {
		t.Fatalf("non-Vertex post-replay sanitized signature = %q, want skip_thought_signature_validator", got)
	}
}

func TestCountTokensReasoningReplayPeeksWithoutReservingTombstone(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.8-flash"
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Headers:        http.Header{"X-Session-Id": []string{"sess-count-tokens-no-tombstone"}},
	}
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"Count tokens only"}]}]}`)
	doc := ojson.NewDoc(payload)
	req := cliproxyexecutor.Request{
		Model:    model,
		Payload:  payload,
		Metadata: map[string]any{"action": "countTokens"},
	}
	scope, replayReq, changed, err := prepareGeminiTopLevelReasoningReplayPayloadDoc(context.Background(), model, req, opts, doc)
	if err != nil || changed || len(replayReq) != 0 {
		t.Fatalf("countTokens prepare = scope:%+v len(replayReq):%d changed:%v err:%v", scope, len(replayReq), changed, err)
	}
	// A subsequent real generation turn must still be able to cache best-effort
	// without being blocked by an unclosed countTokens reservation.
	item := []byte(`{"type":"thought_signature","contentIndex":0,"partIndex":0,"targetOccurrence":0,"thoughtSignature":"EmcKZQFpFH0THU+YNRY5uVwt/0bo2hFo1RmXsMhlX9FCg2n6GssBe5sMV37KB6OrgeKKEP9fxN261QX"}`)
	if !internalcache.CacheAntigravityReasoningReplayItems(model, scope.sessionKey, [][]byte{item}) {
		t.Fatal("failed to cache replay item after countTokens peek")
	}
	got, ok := internalcache.GetAntigravityReasoningReplayItems(model, scope.sessionKey)
	if !ok || len(got) != 1 {
		t.Fatalf("GetAntigravityReasoningReplayItems after countTokens peek = %v, ok=%v", got, ok)
	}
}

func TestAntigravityReasoningReplayScopeIsolatesBodySubagentsAndCanonicalSession(t *testing.T) {
	const model = "gemini-3.8-flash-high"

	t.Run("camelCase_sessionId_with_metadata_agent_id_enriched_and_direct", func(t *testing.T) {
		payload1 := []byte(`{"sessionId":"root-swarm","metadata":{"agent_id":"worker-1"},"request":{"contents":[{"role":"user","parts":[{"text":"shared swarm prompt"}]}]}}`)
		payload2 := []byte(`{"sessionId":"root-swarm","metadata":{"agent_id":"worker-2"},"request":{"contents":[{"role":"user","parts":[{"text":"shared swarm prompt"}]}]}}`)

		req1 := cliproxyexecutor.Request{Model: model, Payload: payload1}
		req2 := cliproxyexecutor.Request{Model: model, Payload: payload2}
		opts1 := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:root-swarm:agent:worker-1",
			},
		}
		opts2 := cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:root-swarm:agent:worker-2",
			},
		}

		scope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, opts1, req1.Payload)
		scope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, opts2, req2.Payload)
		if !scope1.valid() || !scope2.valid() {
			t.Fatalf("expected valid scopes, got scope1=%+v scope2=%+v", scope1, scope2)
		}
		if scope1.sessionKey == scope2.sessionKey {
			t.Fatalf("parallel workers collided on replay scope: %q", scope1.sessionKey)
		}
		if !strings.Contains(scope1.sessionKey, "worker-1") || !strings.Contains(scope2.sessionKey, "worker-2") {
			t.Fatalf("expected agent IDs in replay scopes, got scope1=%q scope2=%q", scope1.sessionKey, scope2.sessionKey)
		}

		docScope1 := antigravityReasoningReplayScopeFromRequestDoc(context.Background(), model, req1, opts1, ojson.NewDoc(payload1))
		docScope2 := antigravityReasoningReplayScopeFromRequestDoc(context.Background(), model, req2, opts2, ojson.NewDoc(payload2))
		if docScope1.sessionKey != scope1.sessionKey || docScope2.sessionKey != scope2.sessionKey {
			t.Fatalf("doc scopes diverged: doc=(%q,%q) raw=(%q,%q)", docScope1.sessionKey, docScope2.sessionKey, scope1.sessionKey, scope2.sessionKey)
		}

		// Direct unenriched calls without CanonicalSessionIDMetadataKey must still isolate
		// via body session extraction and match the enriched key format.
		directScope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, cliproxyexecutor.Options{}, req1.Payload)
		directScope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, cliproxyexecutor.Options{}, req2.Payload)
		if directScope1.sessionKey == directScope2.sessionKey {
			t.Fatalf("unenriched parallel workers collided on replay scope: %q", directScope1.sessionKey)
		}
		if directScope1.sessionKey != scope1.sessionKey || directScope2.sessionKey != scope2.sessionKey {
			t.Fatalf("unenriched keys diverged from canonical keys: direct=(%q,%q) canonical=(%q,%q)",
				directScope1.sessionKey, directScope2.sessionKey, scope1.sessionKey, scope2.sessionKey)
		}
	})

	t.Run("snake_case_session_id_with_metadata_subagent_id", func(t *testing.T) {
		payload1 := []byte(`{"session_id":"root-swarm","metadata":{"subagent_id":"sub-1"},"request":{"contents":[{"role":"user","parts":[{"text":"shared swarm prompt"}]}]}}`)
		payload2 := []byte(`{"session_id":"root-swarm","metadata":{"subagent_id":"sub-2"},"request":{"contents":[{"role":"user","parts":[{"text":"shared swarm prompt"}]}]}}`)

		req1, opts1 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payload1}, cliproxyexecutor.Options{})
		req2, opts2 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payload2}, cliproxyexecutor.Options{})

		enriched1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, opts1, payload1)
		enriched2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, opts2, payload2)
		if enriched1.sessionKey == enriched2.sessionKey {
			t.Fatalf("snake_case session_id subagents collided: %q", enriched1.sessionKey)
		}
		if !strings.Contains(enriched1.sessionKey, "sub-1") || !strings.Contains(enriched2.sessionKey, "sub-2") {
			t.Fatalf("expected subagent IDs in replay scopes, got %q vs %q", enriched1.sessionKey, enriched2.sessionKey)
		}

		direct1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload1}, cliproxyexecutor.Options{}, payload1)
		direct2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload2}, cliproxyexecutor.Options{}, payload2)
		if direct1.sessionKey != enriched1.sessionKey || direct2.sessionKey != enriched2.sessionKey {
			t.Fatalf("unenriched snake_case subagent keys diverged: direct=(%q,%q) enriched=(%q,%q)",
				direct1.sessionKey, direct2.sessionKey, enriched1.sessionKey, enriched2.sessionKey)
		}
	})

	t.Run("request_metadata_canonical_session_id_fallback", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template"}]}]}}`)
		req1 := cliproxyexecutor.Request{
			Model:    model,
			Payload:  payload,
			Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:swarm:agent:req-1"},
		}
		req2 := cliproxyexecutor.Request{
			Model:    model,
			Payload:  payload,
			Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:swarm:agent:req-2"},
		}
		scope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, cliproxyexecutor.Options{}, payload)
		scope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, cliproxyexecutor.Options{}, payload)
		if scope1.sessionKey == scope2.sessionKey || !strings.Contains(scope1.sessionKey, "req-1") || !strings.Contains(scope2.sessionKey, "req-2") {
			t.Fatalf("req.Metadata CanonicalSessionIDMetadataKey not isolated: %q vs %q", scope1.sessionKey, scope2.sessionKey)
		}
	})

	t.Run("claude_code_body_agent_id_preserves_lane_and_isolates_workers", func(t *testing.T) {
		headers := http.Header{"X-Claude-Code-Session-Id": []string{"claude-swarm-sess"}}
		payload1 := []byte(`{"metadata":{"agent_id":"worker-1"},"system":[{"type":"text","text":"You are Claude Code."}],"messages":[{"role":"user","content":"hi"}]}`)
		payload2 := []byte(`{"metadata":{"user_id":"{\"session_id\":\"claude-swarm-sess\",\"agent_id\":\"worker-2\"}"},"system":[{"type":"text","text":"You are Claude Code."}],"messages":[{"role":"user","content":"hi"}]}`)

		scope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload1}, cliproxyexecutor.Options{Headers: headers}, payload1)
		scope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload2}, cliproxyexecutor.Options{Headers: headers}, payload2)
		if scope1.sessionKey == scope2.sessionKey {
			t.Fatalf("Claude body subagents collided: %q", scope1.sessionKey)
		}
		if !strings.HasPrefix(scope1.sessionKey, "claude:claude-swarm-sess:agent:worker-1:context:") ||
			!strings.HasPrefix(scope2.sessionKey, "claude:claude-swarm-sess:agent:worker-2:context:") {
			t.Fatalf("unexpected Claude body subagent keys: %q vs %q", scope1.sessionKey, scope2.sessionKey)
		}
	})

	t.Run("canonical_prefix_normalization_matches_existing_namespaces", func(t *testing.T) {
		plain := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:plain-id"},
		}, nil)
		if plain.sessionKey != "session:plain-id" {
			t.Fatalf("canonical session:* key = %q, want session:plain-id", plain.sessionKey)
		}
		execOnly := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "execution:exec-only"},
		}, nil)
		if execOnly.sessionKey != "execution:exec-only" {
			t.Fatalf("canonical execution:* key = %q, want execution:exec-only", execOnly.sessionKey)
		}
		derivedOnly := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "derived:ctx:v1:abc"},
		}, nil)
		if derivedOnly.sessionKey != "derived:ctx:v1:abc" {
			t.Fatalf("canonical derived:* key = %q, want derived:ctx:v1:abc", derivedOnly.sessionKey)
		}
	})
}

func TestAntigravityReasoningReplayScopeIsolatesBodyTaskThreadConversationAndLCP(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const model = "gemini-3.8-flash-high"

	cases := []struct {
		name      string
		payloadA  []byte
		payloadB  []byte
		optsA     cliproxyexecutor.Options
		optsB     cliproxyexecutor.Options
		enrich    bool
		wantPartA string
		wantPartB string
	}{
		{
			name:      "task_id_body_direct",
			payloadA:  []byte(`{"task_id":"task-alpha","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB:  []byte(`{"task_id":"task-beta","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			wantPartA: "task:task-alpha",
			wantPartB: "task:task-beta",
		},
		{
			name:      "task_id_body_enriched",
			payloadA:  []byte(`{"task_id":"task-alpha","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB:  []byte(`{"task_id":"task-beta","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			enrich:    true,
			wantPartA: "task:task-alpha",
			wantPartB: "task:task-beta",
		},
		{
			name:      "thread_id_body_direct",
			payloadA:  []byte(`{"thread_id":"thread-alpha","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB:  []byte(`{"thread_id":"thread-beta","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			wantPartA: "thread:thread-alpha",
			wantPartB: "thread:thread-beta",
		},
		{
			name:      "conversation_id_body_direct",
			payloadA:  []byte(`{"conversation_id":"conv-alpha","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB:  []byte(`{"conversation_id":"conv-beta","request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			wantPartA: "conv:conv-alpha",
			wantPartB: "conv:conv-beta",
		},
		{
			name:     "lcp_canonical_session_without_derived",
			payloadA: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			optsA: cliproxyexecutor.Options{
				Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:branch-alpha"},
			},
			optsB: cliproxyexecutor.Options{
				Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:branch-beta"},
			},
			wantPartA: "lcp:v1:branch-alpha",
			wantPartB: "lcp:v1:branch-beta",
		},
		{
			name:     "lcp_canonical_session_overrides_shared_derived",
			payloadA: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			optsA: cliproxyexecutor.Options{
				Metadata: map[string]any{
					cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:branch-alpha",
					cliproxyexecutor.DerivedSessionIDMetadataKey:   "ctx:v1:shared-root",
				},
			},
			optsB: cliproxyexecutor.Options{
				Metadata: map[string]any{
					cliproxyexecutor.CanonicalSessionIDMetadataKey: "lcp:v1:branch-beta",
					cliproxyexecutor.DerivedSessionIDMetadataKey:   "ctx:v1:shared-root",
				},
			},
			wantPartA: "lcp:v1:branch-alpha",
			wantPartB: "lcp:v1:branch-beta",
		},
		{
			name:     "lcp_affinity_metadata_without_canonical",
			payloadA: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			payloadB: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`),
			optsA: cliproxyexecutor.Options{
				Metadata: map[string]any{cliproxyexecutor.LCPAffinitySessionIDMetadataKey: "lcp:v1:affinity-alpha"},
			},
			optsB: cliproxyexecutor.Options{
				Metadata: map[string]any{cliproxyexecutor.LCPAffinitySessionIDMetadataKey: "lcp:v1:affinity-beta"},
			},
			wantPartA: "lcp:v1:affinity-alpha",
			wantPartB: "lcp:v1:affinity-beta",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqA := cliproxyexecutor.Request{Model: model, Payload: tc.payloadA}
			reqB := cliproxyexecutor.Request{Model: model, Payload: tc.payloadB}
			optsA, optsB := tc.optsA, tc.optsB
			if tc.enrich {
				reqA, optsA = cliproxysession.Enrich(reqA, optsA)
				reqB, optsB = cliproxysession.Enrich(reqB, optsB)
			}
			scopeA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqA, optsA, tc.payloadA)
			scopeB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqB, optsB, tc.payloadB)
			if !scopeA.valid() || !scopeB.valid() {
				t.Fatalf("expected valid scopes, got A=%+v B=%+v", scopeA, scopeB)
			}
			if scopeA.sessionKey == scopeB.sessionKey {
				t.Fatalf("parallel agents collided on %q", scopeA.sessionKey)
			}
			if !strings.Contains(scopeA.sessionKey, tc.wantPartA) || !strings.Contains(scopeB.sessionKey, tc.wantPartB) {
				t.Fatalf("unexpected keys: A=%q (want %q), B=%q (want %q)", scopeA.sessionKey, tc.wantPartA, scopeB.sessionKey, tc.wantPartB)
			}
		})
	}

	// End-to-end replay isolation: committing worker-1's thoughtSignature must not
	// leak into worker-2 sharing the same prompt template and parent sessionId.
	turn1Worker1 := []byte(`{"sessionId":"root-swarm","metadata":{"agent_id":"worker-1"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
	turn2Worker2 := []byte(`{"sessionId":"root-swarm","metadata":{"agent_id":"worker-2"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]},{"role":"model","parts":[{"text":"same reply"}]},{"role":"user","parts":[{"text":"next"}]}]}}`)
	req1, opts1 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: turn1Worker1}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	req2, opts2 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: turn2Worker2}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})

	_, scope1, errPrepare1 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req1, opts1, turn1Worker1)
	if errPrepare1 != nil {
		t.Fatalf("prepare worker-1: %v", errPrepare1)
	}
	acc1 := newAntigravityReasoningReplayAccumulator(scope1, turn1Worker1)
	acc1.observeResponsePayload([]byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"same reply"},{"text":"","thoughtSignature":"worker-1-private-signature-123456"}]},"finishReason":"STOP"}]}}`))
	acc1.Commit(context.Background())

	out2, _, errPrepare2 := prepareAntigravityGeminiReasoningReplayPayload(context.Background(), model, req2, opts2, turn2Worker2)
	if errPrepare2 != nil {
		t.Fatalf("prepare worker-2: %v", errPrepare2)
	}
	if leaked := ojson.GetBytes(out2, "request.contents.1.parts.0.thoughtSignature").String(); leaked != "" {
		t.Fatalf("worker-1 thoughtSignature leaked into worker-2 replay payload: %q", leaked)
	}
}

func TestAntigravityReasoningReplayScopeIsolatesCodexSubagentsSharingSessionIDHeader(t *testing.T) {
	const model = "gemini-3.8-flash-high"
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"codex swarm prompt"}]}]}}`)

	headers1 := http.Header{
		"Session-Id":            []string{"codex-root-sess"},
		"X-Codex-Turn-Metadata": []string{`{"agent_name":"worker-1","subagent_kind":"thread_spawn"}`},
	}
	headers2 := http.Header{
		"Session-Id":            []string{"codex-root-sess"},
		"X-Codex-Turn-Metadata": []string{`{"agent_name":"worker-2","subagent_kind":"thread_spawn"}`},
	}

	// Direct unenriched call
	scope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{Headers: headers1}, payload)
	scope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{Headers: headers2}, payload)
	if scope1.sessionKey == scope2.sessionKey {
		t.Fatalf("Codex subagents sharing Session-Id collided: %q", scope1.sessionKey)
	}
	if !strings.Contains(scope1.sessionKey, "worker-1") || !strings.Contains(scope2.sessionKey, "worker-2") {
		t.Fatalf("expected Codex agent names in keys, got %q vs %q", scope1.sessionKey, scope2.sessionKey)
	}

	// Enriched call must match unenriched key
	req1, opts1 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{Headers: headers1})
	req2, opts2 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{Headers: headers2})
	enriched1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, opts1, payload)
	enriched2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, opts2, payload)
	if enriched1.sessionKey != scope1.sessionKey || enriched2.sessionKey != scope2.sessionKey {
		t.Fatalf("enriched Codex subagent keys diverged: enriched=(%q,%q) direct=(%q,%q)",
			enriched1.sessionKey, enriched2.sessionKey, scope1.sessionKey, scope2.sessionKey)
	}

	// Normal single-agent Session-Id with benign turn metadata keeps responses:<id>
	normalHeaders := http.Header{
		"Session-Id":            []string{"codex-root-sess"},
		"X-Codex-Turn-Metadata": []string{`{"turn_id":"turn-1"}`},
	}
	normalReq, normalOpts := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{Headers: normalHeaders})
	normalScope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, normalReq, normalOpts, payload)
	if normalScope.sessionKey != "responses:codex-root-sess" {
		t.Fatalf("normal Session-Id replay scope = %q, want responses:codex-root-sess", normalScope.sessionKey)
	}
}

func TestAntigravityReasoningReplayScopePreservesPriorityInvariantsWithCanonicalMetadata(t *testing.T) {
	const model = "gemini-3.8-flash-high"

	// 1. ExecutionSessionMetadataKey remains ahead of prompt_cache_key even after session.Enrich sets pck:* canonical ID.
	pckPayload := []byte(`{"prompt_cache_key":"shared-cache-bucket"}`)
	reqExec, optsExec := cliproxysession.Enrich(
		cliproxyexecutor.Request{Model: model, Payload: pckPayload},
		cliproxyexecutor.Options{
			OriginalRequest: pckPayload,
			Metadata:        map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "socket-session"},
		},
	)
	execScope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqExec, optsExec, nil)
	if execScope.sessionKey != "execution:socket-session" {
		t.Fatalf("enriched execution+prompt_cache_key scope = %q, want execution:socket-session", execScope.sessionKey)
	}

	// 2. prompt_cache_key alone preserves prompt-cache:<value> namespace even after session.Enrich.
	reqPCK, optsPCK := cliproxysession.Enrich(
		cliproxyexecutor.Request{Model: model, Payload: []byte(`{"prompt_cache_key":"same-value"}`)},
		cliproxyexecutor.Options{OriginalRequest: []byte(`{"prompt_cache_key":"same-value"}`)},
	)
	pckScope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqPCK, optsPCK, nil)
	if pckScope.sessionKey != "prompt-cache:same-value" {
		t.Fatalf("enriched prompt_cache_key scope = %q, want prompt-cache:same-value", pckScope.sessionKey)
	}

	// 3. Claude Code keeps :context:<lane> even when CanonicalSessionIDMetadataKey is populated.
	claudePayload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"system":[{"type":"text","text":"You are Claude Code."}]}`)
	claudeReq, claudeOpts := cliproxysession.Enrich(
		cliproxyexecutor.Request{Model: model, Payload: claudePayload},
		cliproxyexecutor.Options{
			Headers:         http.Header{"X-Claude-Code-Session-Id": []string{"claude-sess-priority"}},
			OriginalRequest: claudePayload,
		},
	)
	claudeScope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, claudeReq, claudeOpts, nil)
	if !strings.HasPrefix(claudeScope.sessionKey, "claude:claude-sess-priority:agent:main:context:") {
		t.Fatalf("enriched Claude Code scope = %q, want claude:claude-sess-priority:agent:main:context:<lane>", claudeScope.sessionKey)
	}

	// 4. CallerScope still isolates identical CanonicalSessionIDMetadataKey values across callers.
	callerA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:shared:agent:worker-1",
			cliproxyexecutor.CallerScopeMetadataKey:        cliproxysession.CallerScope("caller-A"),
		},
	}, nil)
	callerB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "session:shared:agent:worker-1",
			cliproxyexecutor.CallerScopeMetadataKey:        cliproxysession.CallerScope("caller-B"),
		},
	}, nil)
	if !callerA.valid() || !callerB.valid() || callerA.sessionKey == callerB.sessionKey {
		t.Fatalf("caller isolation failed for canonical sessions: A=%q B=%q", callerA.sessionKey, callerB.sessionKey)
	}
	if !strings.Contains(callerA.sessionKey, "session:shared:agent:worker-1") || !strings.Contains(callerB.sessionKey, "session:shared:agent:worker-1") {
		t.Fatalf("caller-scoped canonical keys dropped canonical session ID: A=%q B=%q", callerA.sessionKey, callerB.sessionKey)
	}
}

func TestAntigravityReasoningReplayScopeReviewFindingsRegressions(t *testing.T) {
	const model = "gemini-3.8-flash-high"

	t.Run("shared_session_id_with_distinct_thread_id_enriched_and_direct", func(t *testing.T) {
		payloadA := []byte(`{"session_id":"root","thread_id":"thread-A","request":{"contents":[{"role":"user","parts":[{"text":"shared prompt"}]}]}}`)
		payloadB := []byte(`{"session_id":"root","thread_id":"thread-B","request":{"contents":[{"role":"user","parts":[{"text":"shared prompt"}]}]}}`)

		reqA, optsA := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payloadA}, cliproxyexecutor.Options{})
		reqB, optsB := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: payloadB}, cliproxyexecutor.Options{})

		enrichedA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqA, optsA, payloadA)
		enrichedB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqB, optsB, payloadB)
		if enrichedA.sessionKey == enrichedB.sessionKey {
			t.Fatalf("enriched shared session_id with distinct thread_id collided: %q", enrichedA.sessionKey)
		}
		if !strings.Contains(enrichedA.sessionKey, "thread:thread-A") || !strings.Contains(enrichedB.sessionKey, "thread:thread-B") {
			t.Fatalf("expected thread-scoped keys, got A=%q B=%q", enrichedA.sessionKey, enrichedB.sessionKey)
		}

		directA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payloadA}, cliproxyexecutor.Options{}, payloadA)
		directB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, cliproxyexecutor.Request{Model: model, Payload: payloadB}, cliproxyexecutor.Options{}, payloadB)
		if directA.sessionKey == directB.sessionKey {
			t.Fatalf("direct shared session_id with distinct thread_id collided: %q", directA.sessionKey)
		}
		if directA.sessionKey != enrichedA.sessionKey || directB.sessionKey != enrichedB.sessionKey {
			t.Fatalf("direct and enriched thread keys diverged: direct=(%q,%q) enriched=(%q,%q)",
				directA.sessionKey, directB.sessionKey, enrichedA.sessionKey, enrichedB.sessionKey)
		}
	})

	t.Run("plain_user_id_does_not_override_execution_session_or_collapse_conversations", func(t *testing.T) {
		payload := []byte(`{"metadata":{"user_id":"alice"},"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)

		reqA, optsA := cliproxysession.Enrich(
			cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-A"}},
		)
		reqB, optsB := cliproxysession.Enrich(
			cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-B"}},
		)
		enrichedA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqA, optsA, payload)
		enrichedB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqB, optsB, payload)
		if enrichedA.sessionKey != "execution:exec-A" || enrichedB.sessionKey != "execution:exec-B" {
			t.Fatalf("enriched plain metadata.user_id overrode execution sessions: A=%q B=%q", enrichedA.sessionKey, enrichedB.sessionKey)
		}

		directA := antigravityReasoningReplayScopeFromRequest(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-A"}},
			payload,
		)
		directB := antigravityReasoningReplayScopeFromRequest(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: payload},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-B"}},
			payload,
		)
		if directA.sessionKey != "execution:exec-A" || directB.sessionKey != "execution:exec-B" {
			t.Fatalf("direct plain metadata.user_id overrode execution sessions: A=%q B=%q", directA.sessionKey, directB.sessionKey)
		}

		// Without execution session, two unrelated conversations for the same plain user_id
		// must not collapse onto a shared user:alice replay key.
		conv1 := []byte(`{"metadata":{"user_id":"alice"},"request":{"contents":[{"role":"user","parts":[{"text":"topic one"}]}]}}`)
		conv2 := []byte(`{"metadata":{"user_id":"alice"},"request":{"contents":[{"role":"user","parts":[{"text":"topic two"}]}]}}`)
		req1, opts1 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: conv1}, cliproxyexecutor.Options{})
		req2, opts2 := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: conv2}, cliproxyexecutor.Options{})
		scope1 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req1, opts1, conv1)
		scope2 := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req2, opts2, conv2)
		if scope1.sessionKey == scope2.sessionKey || strings.Contains(scope1.sessionKey, "user:alice") {
			t.Fatalf("plain user_id collapsed distinct conversations: scope1=%q scope2=%q", scope1.sessionKey, scope2.sessionKey)
		}
	})

	t.Run("standalone_fork_source_without_child_session_skips_replay", func(t *testing.T) {
		forkA := []byte(`{"forkSource":{"sessionId":"parent-A"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
		forkB := []byte(`{"forkSource":{"sessionId":"parent-B"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
		nestedFork := []byte(`{"request":{"forkSource":{"sessionId":"parent-C"},"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)

		for _, tc := range []struct {
			name    string
			payload []byte
			enrich  bool
		}{
			{name: "forkA_direct", payload: forkA},
			{name: "forkA_enriched", payload: forkA, enrich: true},
			{name: "forkB_direct", payload: forkB},
			{name: "nested_fork_direct", payload: nestedFork},
		} {
			req := cliproxyexecutor.Request{Model: model, Payload: tc.payload}
			opts := cliproxyexecutor.Options{}
			if tc.enrich {
				req, opts = cliproxysession.Enrich(req, opts)
			}
			scope := antigravityReasoningReplayScopeFromRequest(context.Background(), model, req, opts, tc.payload)
			if scope.valid() {
				t.Fatalf("%s: expected replay to be skipped for unidentified fork, got %q", tc.name, scope.sessionKey)
			}
			docScope := antigravityReasoningReplayScopeFromRequestDoc(context.Background(), model, req, opts, ojson.NewDoc(tc.payload))
			if docScope.valid() {
				t.Fatalf("%s (doc): expected replay to be skipped for unidentified fork, got %q", tc.name, docScope.sessionKey)
			}
		}

		// IsForkMetadataKey without any child session identity must also skip replay.
		plainBody := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
		metaForkScope := antigravityReasoningReplayScopeFromRequest(
			context.Background(),
			model,
			cliproxyexecutor.Request{Model: model, Payload: plainBody},
			cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.IsForkMetadataKey: true}},
			plainBody,
		)
		if metaForkScope.valid() {
			t.Fatalf("IsForkMetadataKey without child session should skip replay, got %q", metaForkScope.sessionKey)
		}

		// A fork with an explicit child sessionId remains isolated and valid.
		childForkA := []byte(`{"sessionId":"child-A","forkSource":{"sessionId":"parent-A"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
		childForkB := []byte(`{"sessionId":"child-B","forkSource":{"sessionId":"parent-A"},"request":{"contents":[{"role":"user","parts":[{"text":"shared template prompt"}]}]}}`)
		reqChildA, optsChildA := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: childForkA}, cliproxyexecutor.Options{})
		reqChildB, optsChildB := cliproxysession.Enrich(cliproxyexecutor.Request{Model: model, Payload: childForkB}, cliproxyexecutor.Options{})
		scopeChildA := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqChildA, optsChildA, childForkA)
		scopeChildB := antigravityReasoningReplayScopeFromRequest(context.Background(), model, reqChildB, optsChildB, childForkB)
		if !scopeChildA.valid() || !scopeChildB.valid() || scopeChildA.sessionKey == scopeChildB.sessionKey {
			t.Fatalf("identified child forks should stay valid and isolated: A=%+v B=%+v", scopeChildA, scopeChildB)
		}
	})
}

func TestAntigravityReasoningReplay_PrunesEmptyExtraContentShell(t *testing.T) {
	t.Run("sequential_remove_other_parts_prunes_empty_and_preserves_siblings", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"thought":true,"text":"plan","thoughtSignature":"sig-dup"},{"text":"dup-empty","extra_content":{"google":{"thought_signature":"sig-dup"}}},{"text":"dup-google-sibling","extra_content":{"google":{"thought_signature":"sig-dup","keep":"yes"}}},{"text":"dup-extra-sibling","extra_content":{"google":{"thought_signature":"sig-dup"},"keep":"yes"}}]}]}}`)
		out := antigravityRemoveThoughtSignatureFromOtherParts(payload, 0, "sig-dup", "request.contents.0.parts.0")

		p1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1")
		if p1.Get("extra_content").Exists() {
			t.Fatalf("parts.1 left empty extra_content shell: %s", p1.Raw)
		}
		p2 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.2")
		if p2.Get("extra_content.google.thought_signature").Exists() || p2.Get("extra_content.google.keep").String() != "yes" {
			t.Fatalf("parts.2 should keep non-empty extra_content.google sibling: %s", p2.Raw)
		}
		p3 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.3")
		if p3.Get("extra_content.google").Exists() || p3.Get("extra_content.keep").String() != "yes" {
			t.Fatalf("parts.3 should prune empty google but keep extra_content sibling: %s", p3.Raw)
		}
	})

	t.Run("batch_remove_other_parts_prunes_empty_extra_content", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"thought":true,"text":"plan"},{"text":"dup","extra_content":{"google":{"thought_signature":"sig-dup"}}}]}]}}`)
		batch := newAntigravityReplayBatch(newAntigravityReplayRequestIndex(payload))
		keep := antigravityReplayPartKey{contentIndex: 0, partIndex: 0}
		if !batch.removeThoughtSignatureFromOtherParts(0, "sig-dup", keep) {
			t.Fatal("expected removeThoughtSignatureFromOtherParts to report change")
		}
		out, ok := batch.build(payload)
		if !ok {
			t.Fatal("expected batch.build to succeed")
		}
		p1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1")
		if p1.Get("extra_content").Exists() {
			t.Fatalf("batch parts.1 left empty extra_content shell: %s", p1.Raw)
		}
	})

	t.Run("batch_apply_function_call_signature_prunes_empty_extra_content", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"q":"go"}},"extra_content":{"google":{"thought_signature":"sig-fc"}}},{"text":"dup","extra_content":{"google":{"thought_signature":"sig-fc"}}}]}]}}`)
		item := ojson.Parse(`{"type":"function_call_part","call_id":"call-1","name":"lookup","args":{"q":"go"},"thoughtSignature":"sig-fc"}`)
		batch := newAntigravityReplayBatch(newAntigravityReplayRequestIndex(payload))
		if !batch.applyFunctionCallSignature(item, nil) {
			t.Fatal("expected batch.applyFunctionCallSignature to succeed")
		}
		out, ok := batch.build(payload)
		if !ok {
			t.Fatal("expected batch.build to succeed")
		}
		p0 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0")
		if p0.Get("thoughtSignature").String() != "sig-fc" || p0.Get("extra_content").Exists() {
			t.Fatalf("batch parts.0 expected canonical thoughtSignature without empty extra_content: %s", p0.Raw)
		}
		p1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1")
		if p1.Get("extra_content").Exists() {
			t.Fatalf("batch parts.1 left empty extra_content shell: %s", p1.Raw)
		}
	})

	t.Run("sequential_restore_function_call_prunes_empty_extra_content", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{"q":"go"}},"extra_content":{"google":{"thought_signature":"sig-fc"}}},{"text":"dup","extra_content":{"google":{"thought_signature":"sig-fc"}}}]}]}}`)
		item := ojson.Parse(`{"type":"function_call_part","call_id":"call-1","name":"lookup","args":{"q":"go"},"thoughtSignature":"sig-fc"}`)
		out, changed := restoreAntigravityNativeFunctionCallReplay(payload, 0, 0, item, false, true)
		if !changed {
			t.Fatal("expected restoreAntigravityNativeFunctionCallReplay to report change")
		}
		p0 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0")
		if p0.Get("thoughtSignature").String() != "sig-fc" || p0.Get("extra_content").Exists() {
			t.Fatalf("sequential parts.0 expected canonical thoughtSignature without empty extra_content: %s", p0.Raw)
		}
		p1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1")
		if p1.Get("extra_content").Exists() {
			t.Fatalf("sequential parts.1 left empty extra_content shell: %s", p1.Raw)
		}
	})

	t.Run("merge_function_call_fallback_prunes_empty_extra_content", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"text":"slot"},{"text":"dup","extra_content":{"google":{"thought_signature":"sig-merge"}}}]}]}}`)
		item := ojson.Parse(`{"type":"function_call_part","call_id":"call-2","name":"lookup","args":{"q":"go"},"thoughtSignature":"sig-merge","contentIndex":0,"partIndex":0}`)
		index := newAntigravityReplayRequestIndex(payload)
		out, changed := mergeAntigravityFunctionCallPartReplayWithSchemas(index, payload, item, nil)
		if !changed {
			t.Fatal("expected mergeAntigravityFunctionCallPartReplayWithSchemas to report change")
		}
		p1 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.1")
		if p1.Get("extra_content").Exists() {
			t.Fatalf("merge fallback parts.1 left empty extra_content shell: %s", p1.Raw)
		}
	})

	t.Run("duplicate_google_and_extra_content_wrappers_pruned_and_siblings_preserved", func(t *testing.T) {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"thought":true,"text":"plan","thoughtSignature":"sig-dup"},{"text":"dup-google-empty","extra_content":{"google":{"thought_signature":"sig-dup"},"google":{}}},{"text":"dup-google-two-sigs","extra_content":{"google":{"thought_signature":"sig-dup"},"google":{"thought_signature":"sig-dup"}}},{"text":"dup-google-keep","extra_content":{"google":{"keep":"yes"},"google":{"thought_signature":"sig-dup"}}},{"text":"dup-extra-two-sigs","extra_content":{"google":{"thought_signature":"sig-dup"}},"extra_content":{"google":{"thought_signature":"sig-dup"}}},{"text":"dup-extra-keep","extra_content":{"google":{"thought_signature":"sig-dup"},"keep":"yes"},"extra_content":{"google":{"thought_signature":"sig-dup"}}}]}]}}`)
		out := antigravityRemoveThoughtSignatureFromOtherParts(payload, 0, "sig-dup", "request.contents.0.parts.0")

		for _, idx := range []int{1, 2, 4} {
			p := ojson.GetBytesNoCopy(out, fmt.Sprintf("request.contents.0.parts.%d", idx))
			if p.Get("extra_content").Exists() || strings.Contains(p.Raw, "thought_signature") {
				t.Fatalf("sequential parts.%d left extra_content or thought_signature: %s", idx, p.Raw)
			}
		}
		p3 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.3")
		if strings.Contains(p3.Raw, "thought_signature") || p3.Get("extra_content.google.keep").String() != "yes" || strings.Count(p3.Raw, `"google"`) != 1 {
			t.Fatalf("sequential parts.3 should strip duplicate google sig and keep non-empty google sibling: %s", p3.Raw)
		}
		p5 := ojson.GetBytesNoCopy(out, "request.contents.0.parts.5")
		if strings.Contains(p5.Raw, "thought_signature") || strings.Contains(p5.Raw, `"google"`) || p5.Get("extra_content.keep").String() != "yes" || strings.Count(p5.Raw, `"extra_content"`) != 1 {
			t.Fatalf("sequential parts.5 should prune empty duplicate extra_content and keep sibling: %s", p5.Raw)
		}

		// Also verify the batch single-part path on duplicate wrappers.
		batch := newAntigravityReplayBatch(newAntigravityReplayRequestIndex(payload))
		keep := antigravityReplayPartKey{contentIndex: 0, partIndex: 0}
		if !batch.removeThoughtSignatureFromOtherParts(0, "sig-dup", keep) {
			t.Fatal("expected batch.removeThoughtSignatureFromOtherParts to report change")
		}
		batchOut, ok := batch.build(payload)
		if !ok {
			t.Fatal("expected batch.build to succeed")
		}
		for _, idx := range []int{1, 2, 4} {
			p := ojson.GetBytesNoCopy(batchOut, fmt.Sprintf("request.contents.0.parts.%d", idx))
			if p.Get("extra_content").Exists() || strings.Contains(p.Raw, "thought_signature") {
				t.Fatalf("batch parts.%d left extra_content or thought_signature: %s", idx, p.Raw)
			}
		}
		bp3 := ojson.GetBytesNoCopy(batchOut, "request.contents.0.parts.3")
		if strings.Contains(bp3.Raw, "thought_signature") || bp3.Get("extra_content.google.keep").String() != "yes" || strings.Count(bp3.Raw, `"google"`) != 1 {
			t.Fatalf("batch parts.3 should strip duplicate google sig and keep non-empty google sibling: %s", bp3.Raw)
		}
		bp5 := ojson.GetBytesNoCopy(batchOut, "request.contents.0.parts.5")
		if strings.Contains(bp5.Raw, "thought_signature") || strings.Contains(bp5.Raw, `"google"`) || bp5.Get("extra_content.keep").String() != "yes" || strings.Count(bp5.Raw, `"extra_content"`) != 1 {
			t.Fatalf("batch parts.5 should prune empty duplicate extra_content and keep sibling: %s", bp5.Raw)
		}
	})
}

func TestPrepareGeminiTopLevelReasoningReplayPayloadDoc_AcceptsGroupedSplitFunctionResponses(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	rawGemini := []byte(`{
		"contents": [
			{"role": "user", "parts": [{"text": "call echo 3 times"}]},
			{"role": "model", "parts": [
				{"functionCall": {"id": "call_1", "name": "echo", "args": {"x": "a"}}},
				{"functionCall": {"id": "call_2", "name": "echo", "args": {"x": "b"}}},
				{"functionCall": {"id": "call_3", "name": "echo", "args": {"x": "c"}}}
			]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"result": "res_a"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"result": "res_b"}}}]},
			{"role": "user", "parts": [{"functionResponse": {"id": "call_3", "name": "echo", "response": {"result": "res_c"}}}]}
		]
	}`)

	translated, errTranslate := translatorgeminigemini.ConvertGeminiRequestToGemini("gemini-4-hydrogen", rawGemini, false)
	if errTranslate != nil {
		t.Fatalf("ConvertGeminiRequestToGemini failed: %v", errTranslate)
	}
	doc := ojson.NewDoc(translated)
	req := cliproxyexecutor.Request{Model: "gemini-4-hydrogen", Payload: rawGemini}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}

	if _, _, _, errPrepare := prepareGeminiTopLevelReasoningReplayPayloadDoc(context.Background(), "gemini-4-hydrogen", req, opts, doc); errPrepare != nil {
		t.Fatalf("expected split parallel functionResponses to pass top-level Gemini replay validation after translation, got: %v", errPrepare)
	}
	if got := doc.Get("contents.2.parts.#").Int(); got != 3 {
		t.Fatalf("expected 3 merged functionResponse parts at contents[2], got %d", got)
	}

	t.Run("snake_case_empty_names_and_duplicate_id_reordering", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			raw  string
		}{
			{
				name: "snake_case_split_with_empty_names",
				raw: `{
					"contents": [
						{"role": "user", "parts": [{"text": "run tools"}]},
						{"role": "model", "parts": [
							{"function_call": {"id": "call_1", "name": "read", "args": {"path": "a"}}},
							{"function_call": {"id": "call_2", "name": "grep", "args": {"pattern": "b"}}}
						]},
						{"role": "user", "parts": [{"function_response": {"id": "call_2", "name": "", "response": {"result": "r2"}}}]},
						{"role": "user", "parts": [{"function_response": {"id": "call_1", "name": "  ", "response": {"result": "r1"}}}]}
					]
				}`,
			},
			{
				name: "duplicate_placeholder_id_reordered_by_name",
				raw: `{
					"contents": [
						{"role": "user", "parts": [{"text": "run tools"}]},
						{"role": "model", "parts": [
							{"functionCall": {"id": "1", "name": "read", "args": {"path": "a"}}},
							{"functionCall": {"id": "1", "name": "grep", "args": {"pattern": "b"}}}
						]},
						{"role": "user", "parts": [
							{"functionResponse": {"id": "1", "name": "grep", "response": {"result": "r2"}}},
							{"functionResponse": {"id": "1", "name": "read", "response": {"result": "r1"}}}
						]}
					]
				}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := []byte(tc.raw)
				tr, errTr := translatorgeminigemini.ConvertGeminiRequestToGemini("gemini-4-hydrogen", raw, false)
				if errTr != nil {
					t.Fatalf("ConvertGeminiRequestToGemini failed: %v", errTr)
				}
				d := ojson.NewDoc(tr)
				r := cliproxyexecutor.Request{Model: "gemini-4-hydrogen", Payload: raw}
				o := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				if _, _, _, errPrep := prepareGeminiTopLevelReasoningReplayPayloadDoc(context.Background(), "gemini-4-hydrogen", r, o, d); errPrep != nil {
					t.Fatalf("expected top-level Gemini replay validation to succeed, got: %v", errPrep)
				}
			})
		}
	})

	t.Run("rejects_empty_boundary_and_excess_split_turns", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			raw     string
			wantErr string
		}{
			{
				name: "empty_parts_boundary",
				raw: `{
					"contents": [
						{"role": "user", "parts": [{"text": "call echo"}]},
						{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {}}}]},
						{"role": "user", "parts": []},
						{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"ok": true}}}]}
					]
				}`,
				wantErr: "content appears before 1 pending functionResponse part(s)",
			},
			{
				name: "excess_split_turns",
				raw: `{
					"contents": [
						{"role": "user", "parts": [{"text": "call echo twice"}]},
						{"role": "model", "parts": [
							{"functionCall": {"id": "call_1", "name": "echo", "args": {}}},
							{"functionCall": {"id": "call_2", "name": "echo", "args": {}}}
						]},
						{"role": "user", "parts": [{"functionResponse": {"id": "call_1", "name": "echo", "response": {"ok": 1}}}]},
						{"role": "user", "parts": [{"functionResponse": {"id": "call_2", "name": "echo", "response": {"ok": 2}}}]},
						{"role": "user", "parts": [{"functionResponse": {"id": "call_3", "name": "echo", "response": {"ok": 3}}}]}
					]
				}`,
				wantErr: "functionResponse count 3 does not match pending functionCall count 2",
			},
			{
				name: "interleaved_user_function_call_and_response",
				raw: `{
					"contents": [
						{"role": "user", "parts": [{"text": "call echo"}]},
						{"role": "model", "parts": [{"functionCall": {"id": "call_1", "name": "echo", "args": {}}}]},
						{"role": "user", "parts": [
							{"functionResponse": {"id": "call_1", "name": "echo", "response": {"ok": true}}},
							{"functionCall": {"id": "call_2", "name": "echo", "args": {}}}
						]}
					]
				}`,
				wantErr: "functionCall and functionResponse parts must not be interleaved in the same content",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				raw := []byte(tc.raw)
				tr, errTr := translatorgeminigemini.ConvertGeminiRequestToGemini("gemini-4-hydrogen", raw, false)
				if errTr != nil {
					t.Fatalf("ConvertGeminiRequestToGemini failed: %v", errTr)
				}
				d := ojson.NewDoc(tr)
				r := cliproxyexecutor.Request{Model: "gemini-4-hydrogen", Payload: raw}
				o := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				_, _, _, errPrep := prepareGeminiTopLevelReasoningReplayPayloadDoc(context.Background(), "gemini-4-hydrogen", r, o, d)
				if errPrep == nil || !strings.Contains(errPrep.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErr, errPrep)
				}
			})
		}
	})
}
