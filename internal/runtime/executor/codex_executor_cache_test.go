package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

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

	httpReq, _, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, cliproxyexecutor.Options{}, rawJSON)
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
	if gotSession := httpReq.Header.Get("session_id"); gotSession != expectedKey {
		t.Fatalf("session_id = %q, want %q", gotSession, expectedKey)
	}
	if got := httpReq.Header.Get("Conversation_id"); got != "" {
		t.Fatalf("Conversation_id = %q, want empty", got)
	}

	httpReq2, _, err := executor.cacheHelper(ctx, nil, sdktranslator.FromString("openai"), url, req, cliproxyexecutor.Options{}, rawJSON)
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

func TestCodexExecutorCacheHelper_OpenAIResponses_PreservesPromptCacheRetention(t *testing.T) {
	executor := &CodexExecutor{}
	url := "https://example.com/responses"
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","prompt_cache_key":"cache-key-1","prompt_cache_retention":"persistent"}`),
	}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true,"prompt_cache_retention":"persistent"}`)

	httpReq, _, err := executor.cacheHelper(context.Background(), nil, sdktranslator.FromString("openai-response"), url, req, cliproxyexecutor.Options{}, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "cache-key-1" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "cache-key-1")
	}
	if got := gjson.GetBytes(body, "prompt_cache_retention").String(); got != "persistent" {
		t.Fatalf("prompt_cache_retention = %q, want %q", got, "persistent")
	}
	if got := httpReq.Header.Get("session_id"); got != "cache-key-1" {
		t.Fatalf("session_id = %q, want %q", got, "cache-key-1")
	}
	if got := httpReq.Header.Get("Conversation_id"); got != "" {
		t.Fatalf("Conversation_id = %q, want empty", got)
	}
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_UsesExecutionSessionForContinuity(t *testing.T) {
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4"}`),
	}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-session-1"}}

	httpReq, _, err := executor.cacheHelper(context.Background(), nil, sdktranslator.FromString("openai"), "https://example.com/responses", req, opts, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "exec-session-1" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "exec-session-1")
	}
	if got := httpReq.Header.Get("session_id"); got != "exec-session-1" {
		t.Fatalf("session_id = %q, want %q", got, "exec-session-1")
	}
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_FallsBackToStableAuthID(t *testing.T) {
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4"}`),
	}
	auth := &cliproxyauth.Auth{ID: "codex-auth-1", Provider: "codex"}

	httpReq, _, err := executor.cacheHelper(context.Background(), auth, sdktranslator.FromString("openai"), "https://example.com/responses", req, cliproxyexecutor.Options{}, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}

	expected := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:auth:codex-auth-1")).String()
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != expected {
		t.Fatalf("prompt_cache_key = %q, want %q", got, expected)
	}
	if got := httpReq.Header.Get("session_id"); got != expected {
		t.Fatalf("session_id = %q, want %q", got, expected)
	}
}

func TestCodexExecutorCacheHelper_ClaudePreservesCacheContinuity(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "claude-3-7-sonnet",
		Payload: []byte(`{"metadata":{"user_id":"user-1"}}`),
	}
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)

	httpReq, continuity, err := executor.cacheHelper(context.Background(), nil, sdktranslator.FromString("claude"), "https://example.com/responses", req, cliproxyexecutor.Options{}, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	if continuity.Key == "" {
		t.Fatal("continuity.Key = empty, want non-empty")
	}
	body, err := io.ReadAll(httpReq.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != continuity.Key {
		t.Fatalf("prompt_cache_key = %q, want %q", got, continuity.Key)
	}
	if got := httpReq.Header.Get("session_id"); got != continuity.Key {
		t.Fatalf("session_id = %q, want %q", got, continuity.Key)
	}
}
