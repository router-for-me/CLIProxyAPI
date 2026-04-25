package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func ctxWithAPIKey(t *testing.T, apiKey string) context.Context {
	t.Helper()
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	if apiKey != "" {
		ginCtx.Set("apiKey", apiKey)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_StablePromptCacheKeyFromAPIKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	expectedKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:test-api-key")).String()
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedKey)
	}
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != expectedKey {
		t.Fatalf("Session_id = %q, want %q", gotSession, expectedKey)
	}

	httpReq2, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error (second call): %v", err)
	}
	body2, errRead2 := io.ReadAll(httpReq2.Body)
	if errRead2 != nil {
		t.Fatalf("read request body (second call): %v", errRead2)
	}
	gotKey2 := gjson.GetBytes(body2, "prompt_cache_key").String()
	if gotKey2 != expectedKey {
		t.Fatalf("prompt_cache_key (second call) = %q, want %q", gotKey2, expectedKey)
	}
}

func assertPromptCacheKey(t *testing.T, executor *CodexExecutor, ctx context.Context, format string, req cliproxyexecutor.Request, rawJSON []byte) string {
	t.Helper()
	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString(format), "https://example.com/responses", req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return gjson.GetBytes(body, "prompt_cache_key").String()
}

func TestCodexExecutorCacheHelper_CallerProvidedKeyIsPassedThrough(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := ctxWithAPIKey(t, "some-api-key")

	payload := []byte(`{"model":"gpt-5","prompt_cache_key":"caller-owned-id","input":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}

	got := assertPromptCacheKey(t, executor, ctx, "openai-response", req, payload)
	if got != "caller-owned-id" {
		t.Fatalf("expected caller-owned id to pass through, got %q", got)
	}
}

func TestCodexExecutorCacheHelper_DifferentConversationsGetDifferentKeys(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := ctxWithAPIKey(t, "api-key-shared")

	convA := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"first question about python"}]}`)
	convB := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"completely different topic about cooking"}]}`)

	keyA := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: convA}, convA)
	keyB := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: convB}, convB)

	if keyA == "" || keyB == "" {
		t.Fatalf("expected non-empty keys, got %q and %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("two different conversations must not share a prompt_cache_key, got %q for both", keyA)
	}
}

func TestCodexExecutorCacheHelper_SameConversationReusesKeyAcrossTurns(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := ctxWithAPIKey(t, "api-key-convos")

	turn1 := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"explain closures"}]}`)
	turn2 := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"explain closures"},{"role":"assistant","content":"..."},{"role":"user","content":"show an example"}]}`)

	keyTurn1 := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: turn1}, turn1)
	keyTurn2 := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: turn2}, turn2)

	if keyTurn1 == "" || keyTurn2 == "" {
		t.Fatalf("expected non-empty keys, got %q and %q", keyTurn1, keyTurn2)
	}
	if keyTurn1 != keyTurn2 {
		t.Fatalf("same conversation must reuse prompt_cache_key across turns: turn1=%q turn2=%q", keyTurn1, keyTurn2)
	}
}

func TestCodexExecutorCacheHelper_ConversationIDFieldPreferredOverContent(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := ctxWithAPIKey(t, "api-key-field")

	p1 := []byte(`{"model":"gpt-5","metadata":{"conversation_id":"conv-42"},"messages":[{"role":"user","content":"first"}]}`)
	p2 := []byte(`{"model":"gpt-5","metadata":{"conversation_id":"conv-42"},"messages":[{"role":"user","content":"second"}]}`)

	k1 := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: p1}, p1)
	k2 := assertPromptCacheKey(t, executor, ctx, "openai", cliproxyexecutor.Request{Model: "gpt-5", Payload: p2}, p2)
	if k1 == "" || k1 != k2 {
		t.Fatalf("explicit conversation_id must win: got %q vs %q", k1, k2)
	}
}

func TestCodexExecutorCacheHelper_DifferentTenantsDoNotCollide(t *testing.T) {
	executor := &CodexExecutor{}

	payload := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}

	k1 := assertPromptCacheKey(t, executor, ctxWithAPIKey(t, "tenant-a"), "openai", req, payload)
	k2 := assertPromptCacheKey(t, executor, ctxWithAPIKey(t, "tenant-b"), "openai", req, payload)
	if k1 == "" || k1 == k2 {
		t.Fatalf("different tenants must not share prompt_cache_key: got %q for both", k1)
	}
}

func TestCodexExecutorCacheHelper_ClaudeUserIDBackwardsCompatible(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := ctxWithAPIKey(t, "")

	payload := []byte(`{"model":"claude-sonnet","metadata":{"user_id":"u-7"},"messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "claude-sonnet", Payload: payload}

	k1 := assertPromptCacheKey(t, executor, ctx, "claude", req, payload)
	k2 := assertPromptCacheKey(t, executor, ctx, "claude", req, payload)
	if k1 == "" || k1 != k2 {
		t.Fatalf("Claude path must keep stable id from metadata.user_id: got %q and %q", k1, k2)
	}
	if _, ok := helps.GetCodexCache("claude-sonnet-u-7"); !ok {
		t.Fatalf("expected legacy claude cache key to be populated")
	}
}

func TestHashCodexDedupeHeaders_IgnoresTraceAndTimingHeaders(t *testing.T) {
	left := http.Header{
		"Content-Type":                          []string{"application/json"},
		"X-Codex-Turn-Metadata":                 []string{`{"turn_id":"turn-left","sandbox":"none"}`},
		"Traceparent":                           []string{"00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"},
		"Tracestate":                            []string{"vendor-a=value-a"},
		"X-Responsesapi-Include-Timing-Metrics": []string{"1"},
		"X-Client-Request-Id":                   []string{"req-left"},
	}
	right := http.Header{
		"Content-Type":                          []string{"application/json"},
		"X-Codex-Turn-Metadata":                 []string{`{"turn_id":"turn-right","sandbox":"none"}`},
		"Traceparent":                           []string{"00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01"},
		"Tracestate":                            []string{"vendor-b=value-b"},
		"X-Responsesapi-Include-Timing-Metrics": []string{"0"},
		"X-Client-Request-Id":                   []string{"req-right"},
	}

	leftHash := hashCodexDedupeHeaders(left)
	rightHash := hashCodexDedupeHeaders(right)
	if leftHash != rightHash {
		t.Fatalf("hashCodexDedupeHeaders() mismatch: left=%q right=%q", leftHash, rightHash)
	}
}

func TestPrepareCodexHTTPCallAppliesHeadersAndPreservesLogBody(t *testing.T) {
	t.Setenv(codexCompressionEnv, "1")

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"account_id": "acct_123"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}
	rawJSON := []byte(`{"model":"gpt-5.4","input":"hello"}`)

	call, err := executor.prepareCodexHTTPCall(
		context.Background(),
		auth,
		sdktranslator.FromString("openai-response"),
		"https://example.com/responses",
		req,
		rawJSON,
		"oauth-token",
		true,
	)
	if err != nil {
		t.Fatalf("prepareCodexHTTPCall() error = %v", err)
	}

	if got := call.prepared.httpReq.Header.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer oauth-token")
	}
	if got := call.prepared.httpReq.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q, want %q", got, "text/event-stream")
	}
	if got := call.prepared.httpReq.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding = %q, want %q", got, "zstd")
	}
	if !bytes.Equal(call.requestLog.Body, call.prepared.body) {
		t.Fatalf("requestLog.Body = %q, want prepared body %q", string(call.requestLog.Body), string(call.prepared.body))
	}
	if id := gjson.GetBytes(call.prepared.body, "client_metadata.x-codex-installation-id").String(); id == "" {
		t.Fatalf("prepared body should include client_metadata.x-codex-installation-id, got %s", call.prepared.body)
	}
	if got := call.requestLog.URL; got != "https://example.com/responses" {
		t.Fatalf("requestLog.URL = %q, want %q", got, "https://example.com/responses")
	}
	if got := call.requestLog.Headers.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("requestLog.Headers[Content-Encoding] = %q, want %q", got, "zstd")
	}
}
