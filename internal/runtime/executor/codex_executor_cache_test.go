package executor

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
	if gotConversation := httpReq.Header.Get("Conversation_id"); gotConversation != expectedKey {
		t.Fatalf("Conversation_id = %q, want %q", gotConversation, expectedKey)
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

func TestCodexPrepareRequestPlan_ReusesBodyAcrossMetadataRetries(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "retry-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"` + string(bytes.Repeat([]byte("hello "), 2048)) + `","stream":true}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
		},
	}

	plan1, err := executor.prepareCodexRequestPlan(ctx, req, opts, codexPreparedRequestPlanExecuteStream)
	if err != nil {
		t.Fatalf("prepareCodexRequestPlan() error = %v", err)
	}
	plan2, err := executor.prepareCodexRequestPlan(ctx, req, opts, codexPreparedRequestPlanExecuteStream)
	if err != nil {
		t.Fatalf("prepareCodexRequestPlan() second error = %v", err)
	}

	if len(plan1.body) == 0 || len(plan2.body) == 0 {
		t.Fatal("expected non-empty cached body")
	}
	if &plan1.body[0] != &plan2.body[0] {
		t.Fatal("expected second plan to reuse cached body backing array")
	}

	expectedKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:retry-api-key")).String()
	if got := gjson.GetBytes(plan1.body, "prompt_cache_key").String(); got != expectedKey {
		t.Fatalf("prompt_cache_key = %q, want %q", got, expectedKey)
	}
	if plan1.conversationID != expectedKey || plan2.conversationID != expectedKey {
		t.Fatalf("conversationID = %q / %q, want %q", plan1.conversationID, plan2.conversationID, expectedKey)
	}
}

func TestCodexPrepareRequestPlan_CacheSeparatesThinkingSuffixes(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "thinking-retry-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	payload := []byte(`{"model":"gpt-5.4","input":"` + string(bytes.Repeat([]byte("hello "), 2048)) + `","stream":true}`)
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
		},
	}

	highPlan, err := executor.prepareCodexRequestPlan(ctx, cliproxyexecutor.Request{
		Model:   "gpt-5.4(high)",
		Payload: payload,
	}, opts, codexPreparedRequestPlanExecuteStream)
	if err != nil {
		t.Fatalf("prepareCodexRequestPlan() high error = %v", err)
	}
	lowPlan, err := executor.prepareCodexRequestPlan(ctx, cliproxyexecutor.Request{
		Model:   "gpt-5.4(low)",
		Payload: payload,
	}, opts, codexPreparedRequestPlanExecuteStream)
	if err != nil {
		t.Fatalf("prepareCodexRequestPlan() low error = %v", err)
	}

	if got := gjson.GetBytes(highPlan.body, "reasoning.effort").String(); got != "high" {
		t.Fatalf("high reasoning.effort = %q, want %q", got, "high")
	}
	if got := gjson.GetBytes(lowPlan.body, "reasoning.effort").String(); got != "low" {
		t.Fatalf("low reasoning.effort = %q, want %q", got, "low")
	}
	if &highPlan.body[0] == &lowPlan.body[0] {
		t.Fatal("expected distinct cached bodies for distinct thinking suffixes")
	}

	cache, ok := opts.Metadata[codexPreparedRequestCacheMetadataKey].(*codexPreparedRequestCache)
	if !ok || cache == nil {
		t.Fatal("expected prepared request cache to be allocated")
	}
	if got := len(cache.entries); got != 2 {
		t.Fatalf("cache entries = %d, want 2", got)
	}
}

func TestCodexPrepareRequestPlan_SkipsCacheForSmallPayloads(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "small-payload-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","stream":true}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
		},
	}

	_, err := executor.prepareCodexRequestPlan(ctx, req, opts, codexPreparedRequestPlanExecuteStream)
	if err != nil {
		t.Fatalf("prepareCodexRequestPlan() error = %v", err)
	}
	if _, ok := opts.Metadata[codexPreparedRequestCacheMetadataKey]; ok {
		t.Fatal("small payload should not allocate prepared request cache")
	}
}

func TestCodexResponseTranslatorNeedsRequestPayloads(t *testing.T) {
	if codexResponseTranslatorNeedsRequestPayloads(sdktranslator.FromString("openai-response")) {
		t.Fatal("openai-response translator should not retain request payloads")
	}
	if !codexResponseTranslatorNeedsRequestPayloads(sdktranslator.FromString("openai")) {
		t.Fatal("openai translator must retain request payloads for tool-name restoration")
	}
}
