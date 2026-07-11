package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractClaudeCodeSessionIDFromPayloadJSON(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"cache-session-1\"}"}}`)
	got := ExtractClaudeCodeSessionID(context.Background(), payload, nil)
	if got != "cache-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want cache-session-1", got)
	}
}

func TestExtractClaudeCodeSessionIDFromHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Request.Header.Set(ClaudeCodeSessionHeader, "header-session-1")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	got := ExtractClaudeCodeSessionID(ctx, []byte(`{"model":"gpt-5.4"}`), nil)
	if got != "header-session-1" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session-1", got)
	}
}

func TestClaudeCodePromptCacheStableAcrossRequests(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"cache-session-2\"}"}}`)
	first, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, nil)
	if err != nil {
		t.Fatalf("ClaudeCodePromptCache first error: %v", err)
	}
	if !ok || first.ID == "" {
		t.Fatalf("ClaudeCodePromptCache first = %#v, ok=%v, want cached id", first, ok)
	}
	second, ok, err := ClaudeCodePromptCache(ctx, "grok-composer-2.5-fast", payload, nil)
	if err != nil {
		t.Fatalf("ClaudeCodePromptCache second error: %v", err)
	}
	if !ok || second.ID != first.ID {
		t.Fatalf("second cache id = %q, want %q", second.ID, first.ID)
	}
}

func TestClaudeCodePromptCacheStableWithoutProcessCacheState(t *testing.T) {
	ctx := context.Background()
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"durable-session\"}"}}`)

	first, ok, err := ClaudeCodePromptCache(ctx, "gpt-5.6-sol(xhigh)", payload, nil)
	if err != nil || !ok {
		t.Fatalf("ClaudeCodePromptCache first = %#v, ok=%v, err=%v", first, ok, err)
	}

	codexCacheMu.Lock()
	codexCacheMap = make(map[string]CodexCache)
	codexCacheMu.Unlock()

	second, ok, err := ClaudeCodePromptCache(ctx, "gpt-5.6-sol(xhigh)", payload, nil)
	if err != nil || !ok {
		t.Fatalf("ClaudeCodePromptCache second = %#v, ok=%v, err=%v", second, ok, err)
	}
	if second.ID != first.ID {
		t.Fatalf("cache ID after reset = %q, want %q", second.ID, first.ID)
	}
}

func TestClaudeCodePromptCacheSeparatesModels(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{\"session_id\":\"model-lane\"}"}}`)
	first, _, _ := ClaudeCodePromptCache(context.Background(), "gpt-5.6-sol(xhigh)", payload, nil)
	second, _, _ := ClaudeCodePromptCache(context.Background(), "gpt-5.6-terra(xhigh)", payload, nil)
	if first.ID == second.ID {
		t.Fatalf("different models share cache ID %q", first.ID)
	}
}

func TestExtractClaudeCodeSessionIDPrefersHeaderOverPayload(t *testing.T) {
	payload := []byte(`{"metadata":{"user_id":"{"session_id":"payload-session"}"}}`)
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "header-session")

	got := ExtractClaudeCodeSessionID(context.Background(), payload, headers)
	if got != "header-session" {
		t.Fatalf("ExtractClaudeCodeSessionID() = %q, want header-session", got)
	}
}
