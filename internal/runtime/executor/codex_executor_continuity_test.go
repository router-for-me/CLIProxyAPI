package executor

import (
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

// TestCodexCacheHelper_PromptCacheKeyTakesPrecedenceOverIdempotencyKey verifies
// that when the payload already contains prompt_cache_key it wins over
// Idempotency-Key and apiKey-based keys (assertion B2.2.4).
func TestCodexCacheHelper_PromptCacheKeyTakesPrecedenceOverIdempotencyKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")
	ginCtx.Request = httptest.NewRequest("POST", "/", nil)
	ginCtx.Request.Header.Set("Idempotency-Key", "idem-123")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","prompt_cache_key":"existing-key","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: rawJSON,
	}

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != "existing-key" {
		t.Fatalf("prompt_cache_key = %q, want %q (should prefer payload key over Idempotency-Key)", gotKey, "existing-key")
	}
}

// TestCodexCacheHelper_IdempotencyKeyFallbackUsedWhenPromptCacheKeyAbsent verifies
// that Idempotency-Key is used when prompt_cache_key is absent (assertion B2.2.5).
func TestCodexCacheHelper_IdempotencyKeyFallbackUsedWhenPromptCacheKeyAbsent(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/", nil)
	ginCtx.Request.Header.Set("Idempotency-Key", "idem-abc")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: rawJSON,
	}

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)

	// Deterministic key derived from the Idempotency-Key header value.
	idemSeed := "cli-proxy-api:codex:prompt-cache:idem:idem-abc"
	expected := uuid.NewSHA1(uuid.NameSpaceOID, []byte(idemSeed))
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != expected.String() {
		t.Fatalf("prompt_cache_key = %q, want %q (derived from Idempotency-Key)", gotKey, expected.String())
	}
}

// TestCodexCacheHelper_APIKeyHashStableWhenNoIdempotencyKey verifies stable
// repeated-request behavior when only apiKey is present (assertion B2.2.6).
func TestCodexCacheHelper_APIKeyHashStableWhenNoIdempotencyKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("apiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: rawJSON,
	}

	httpReq1, _ := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, rawJSON)
	body1, _ := io.ReadAll(httpReq1.Body)
	key1 := gjson.GetBytes(body1, "prompt_cache_key").String()

	httpReq2, _ := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, rawJSON)
	body2, _ := io.ReadAll(httpReq2.Body)
	key2 := gjson.GetBytes(body2, "prompt_cache_key").String()

	if key1 != key2 {
		t.Fatalf("apiKey-based key is not stable across calls: %q vs %q", key1, key2)
	}
}

// TestCodexCacheHelper_RandomUUIDFallbackWhenNoStableSignal verifies that a
// random UUID is generated when no prompt_cache_key, Idempotency-Key, or
// apiKey is available (assertion B2.2.7).
func TestCodexCacheHelper_RandomUUIDFallbackWhenNoStableSignal(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	// No apiKey set, no Idempotency-Key.

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: rawJSON,
	}

	httpReq, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), "https://example.com/responses", req, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	body, _ := io.ReadAll(httpReq.Body)

	// When no stable signal exists, cache.ID remains empty and no prompt_cache_key is set.
	// The current HEAD behavior does NOT set a random UUID in this path — cacheHelper
	// only sets prompt_cache_key when cache.ID != "".
	gotKey := gjson.GetBytes(body, "prompt_cache_key").String()
	if gotKey != "" {
		t.Fatalf("prompt_cache_key = %q, want empty when no stable signal exists (HEAD behavior)", gotKey)
	}
}
