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
	if !bytes.Equal(call.requestLog.Body, rawJSON) {
		t.Fatalf("requestLog.Body = %q, want %q", string(call.requestLog.Body), string(rawJSON))
	}
	if got := call.requestLog.URL; got != "https://example.com/responses" {
		t.Fatalf("requestLog.URL = %q, want %q", got, "https://example.com/responses")
	}
	if got := call.requestLog.Headers.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("requestLog.Headers[Content-Encoding] = %q, want %q", got, "zstd")
	}
}
