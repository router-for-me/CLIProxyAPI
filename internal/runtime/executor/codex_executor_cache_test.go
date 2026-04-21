package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
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
	//nolint:revive // gin context is deliberately stashed under "gin" elsewhere in the codebase.
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

// assertPromptCacheKey extracts prompt_cache_key from a rebuilt request body
// and asserts its value, while also returning it so callers can compare
// across invocations.
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

	// Two independent conversations coming from the *same* api key must not
	// collide. Before the cache-derivation refactor both of these would have
	// produced the api-key-derived UUID and shared upstream prompt cache.
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

	// First turn has just the opening user message; second turn appends
	// assistant + follow-up. The first user message is unchanged, so the
	// fingerprint — and therefore the cache key — should be identical.
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

	// Two payloads with *different* first-user-messages but the *same*
	// explicit conversation_id must collapse to the same cache key.
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

	// Same conversation-level content from two different api keys must
	// *never* collide, otherwise tenant isolation for prompt cache is broken.
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

	// First call populates the cache; second call must reuse the same id.
	k1 := assertPromptCacheKey(t, executor, ctx, "claude", req, payload)
	k2 := assertPromptCacheKey(t, executor, ctx, "claude", req, payload)
	if k1 == "" || k1 != k2 {
		t.Fatalf("Claude path must keep stable id from metadata.user_id: got %q and %q", k1, k2)
	}
	// And the entry must actually be parked under the legacy "model-userID" key.
	if _, ok := helps.GetCodexCache("claude-sonnet-u-7"); !ok {
		t.Fatalf("expected legacy claude cache key to be populated")
	}
}
