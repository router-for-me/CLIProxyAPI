package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func newCodexCacheHelperContext(apiKey string, headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	if apiKey != "" {
		ginCtx.Set("apiKey", apiKey)
	}
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	for name, value := range headers {
		ginCtx.Request.Header.Set(name, value)
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

func TestCodexExecutorCacheHelper_OpenAIResponses_UsesSessionHeaderWhenPayloadKeyMissing(t *testing.T) {
	ctx := newCodexCacheHelperContext("", map[string]string{"X-Session-ID": "cpa:session-123"})
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":[]}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != "cpa:session-123" {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, "cpa:session-123")
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "cpa:session-123" {
		t.Fatalf("Session_id = %q, want %q", gotSession, "cpa:session-123")
	}
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_PrefersPayloadPromptCacheKey(t *testing.T) {
	ctx := newCodexCacheHelperContext("test-api-key", map[string]string{"X-Session-ID": "cpa:header"})
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[],"prompt_cache_key":"cpa:body"}`),
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
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != "cpa:body" {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, "cpa:body")
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "cpa:body" {
		t.Fatalf("Session_id = %q, want %q", gotSession, "cpa:body")
	}
}

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_UsesSessionHeaderBeforeAPIKeyFallback(t *testing.T) {
	ctx := newCodexCacheHelperContext("test-api-key", map[string]string{"X-Session-ID": "cpa:header"})
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[]}`),
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
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != "cpa:header" {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, "cpa:header")
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "cpa:header" {
		t.Fatalf("Session_id = %q, want %q", gotSession, "cpa:header")
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponses_NormalizesDeveloperCurrentTimeForPromptCache(t *testing.T) {
	ctx := newCodexCacheHelperContext("", nil)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true,"prompt_cache_key":"cpa:session","input":[{"role":"developer","content":"You are powered by the model named gpt-5.5. The exact model ID is openai/gpt-5.5\nHere is some useful information about the environment you are running in:\n<env>\n  Working directory: /repo\n  Workspace root folder: /repo\n  Is directory a git repo: yes\n  Platform: linux\n  Today's date: Fri Apr 24 2026\n  Current time: 2026-04-24T05:55:01.054Z\n</env>"},{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","prompt_cache_key":"cpa:session","input":[]}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	content := gjson.GetBytes(body, "input.0.content").String()
	if strings.Contains(content, "2026-04-24T05:55:01.054Z") {
		t.Fatalf("developer current time was not normalized: %q", content)
	}
	if !strings.Contains(content, "Current time: 2026-04-24T00:00:00.000Z") {
		t.Fatalf("developer current time = %q, want normalized day timestamp", content)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponses_DoesNotNormalizeUserAuthoredDeveloperCurrentTime(t *testing.T) {
	ctx := newCodexCacheHelperContext("", nil)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true,"prompt_cache_key":"cpa:session","input":[{"role":"developer","content":"Use this exact timestamp for the audit window.\nCurrent time: 2026-04-24T05:55:01.054Z\nDo not change it."},{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","prompt_cache_key":"cpa:session","input":[]}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	content := gjson.GetBytes(body, "input.0.content").String()
	if !strings.Contains(content, "Current time: 2026-04-24T05:55:01.054Z") {
		t.Fatalf("developer current time was unexpectedly normalized: %q", content)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponses_DoesNotNormalizeStructuredDeveloperContent(t *testing.T) {
	ctx := newCodexCacheHelperContext("", nil)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.5","stream":true,"prompt_cache_key":"cpa:session","input":[{"role":"developer","content":[{"type":"input_text","text":"prefix\n  Current time: 2026-04-24T05:55:01.054Z\nsuffix"}]},{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","prompt_cache_key":"cpa:session","input":[]}`),
	}
	url := "https://example.com/responses"

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	content := gjson.GetBytes(body, "input.0.content")
	if !content.IsArray() {
		t.Fatalf("developer content type changed: %s", string(body))
	}
	if gotText := content.Get("0.text").String(); !strings.Contains(gotText, "2026-04-24T05:55:01.054Z") {
		t.Fatalf("structured developer content was unexpectedly normalized: %q", gotText)
	}
}
