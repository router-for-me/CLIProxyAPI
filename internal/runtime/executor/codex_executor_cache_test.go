package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_DoesNotSynthesizeSessionFromAPIKey(t *testing.T) {
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

	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != "" {
		t.Fatalf("prompt_cache_key = %q, want empty", gotKey)
	}
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "" {
		t.Fatalf("Session_id = %q, want empty", gotSession)
	}
	if gotRequestID := httpReq.Header.Get("X-Client-Request-Id"); gotRequestID != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", gotRequestID)
	}
}

func TestCodexExecutorCacheHelper_PrefersIncomingSessionID(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")
	ginCtx.Request = httptest.NewRequest("POST", "/", nil)
	ginCtx.Request.Header.Set("Session_id", "sess-123")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, []byte(`{"model":"gpt-5.3-codex"}`))
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "sess-123" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "sess-123")
	}
	if got := httpReq.Header.Get("Session_id"); got != "sess-123" {
		t.Fatalf("Session_id = %q, want %q", got, "sess-123")
	}
}

func TestCodexExecutorCacheHelper_UsesPayloadConversationID(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","conversation_id":"conv-123"}`),
	}

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, []byte(`{"model":"gpt-5.3-codex"}`))
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "conv-123" {
		t.Fatalf("prompt_cache_key = %q, want %q", got, "conv-123")
	}
	if got := httpReq.Header.Get("Session_id"); got != "conv-123" {
		t.Fatalf("Session_id = %q, want %q", got, "conv-123")
	}
}

func TestBuildCodexRequestBody_NativeCodexPreservesPayload(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model: "gpt-5.3-codex",
		Payload: []byte(`{
			"model":"alias-model",
			"stream":false,
			"previous_response_id":"resp-123",
			"user":"user-1",
			"context_management":{"compaction":{"type":"auto"}},
			"input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	body, err := buildCodexRequestBody(nil, sdktranslator.FromString("codex"), sdktranslator.FromString("codex"), req, cliproxyexecutor.Options{}, req.Payload, false)
	if err != nil {
		t.Fatalf("buildCodexRequestBody() error = %v", err)
	}

	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.3-codex")
	}
	if got := gjson.GetBytes(body, "stream").Bool(); got {
		t.Fatalf("stream = %v, want false", got)
	}
	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp-123" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp-123")
	}
	if got := gjson.GetBytes(body, "user").String(); got != "user-1" {
		t.Fatalf("user = %q, want %q", got, "user-1")
	}
	if !gjson.GetBytes(body, "context_management").Exists() {
		t.Fatalf("context_management should be preserved")
	}
	if got := gjson.GetBytes(body, "input.0.role").String(); got != "system" {
		t.Fatalf("role = %q, want %q", got, "system")
	}
	if gjson.GetBytes(body, "instructions").Exists() {
		t.Fatalf("instructions should not be synthesized for native codex payloads")
	}
}

func TestBuildCodexRequestBody_NativeCodexPreservesPayloadForStream(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model: "gpt-5.3-codex",
		Payload: []byte(`{
			"model":"alias-model",
			"stream":true,
			"previous_response_id":"resp-456",
			"context_management":{"compaction":{"type":"auto"}},
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	body, err := buildCodexRequestBody(nil, sdktranslator.FromString("codex"), sdktranslator.FromString("codex"), req, cliproxyexecutor.Options{}, req.Payload, true)
	if err != nil {
		t.Fatalf("buildCodexRequestBody() error = %v", err)
	}

	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want %q", got, "gpt-5.3-codex")
	}
	if got := gjson.GetBytes(body, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true", got)
	}
	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp-456" {
		t.Fatalf("previous_response_id = %q, want %q", got, "resp-456")
	}
	if !gjson.GetBytes(body, "context_management").Exists() {
		t.Fatalf("context_management should be preserved")
	}
	if gjson.GetBytes(body, "instructions").Exists() {
		t.Fatalf("instructions should not be synthesized for native codex stream payloads")
	}
}
