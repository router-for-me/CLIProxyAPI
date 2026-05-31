package executor

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCacheHelper_OpenAIChatCompletions_StablePromptCacheKeyFromAPIKey(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Set("userApiKey", "test-api-key")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5.3-codex","stream":true}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex"}`),
	}
	url := "https://example.com/responses"

	httpReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, nil, req, req.Payload, rawJSON)
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
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "" {
		t.Fatalf("Session_id = %q, want empty", gotSession)
	}

	httpReq2, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai"), url, nil, req, req.Payload, rawJSON)
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

func TestCodexExecutorCacheHelper_IdentityConfuseRemapsBodyAndHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ginCtx.Request.Header.Set("Session_id", "client-session")
	ginCtx.Request.Header.Set("X-Codex-Turn-Metadata", `{"prompt_cache_key":"cache-1","turn_id":"turn-1","window_id":"cache-1:0"}`)
	ginCtx.Request.Header.Set("X-Client-Request-Id", "client-request-1")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{cfg: &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true,"client_metadata":{"x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-1\",\"turn_id\":\"turn-1\",\"window_id\":\"cache-1:0\"}","x-codex-window-id":"cache-1:0"}}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-1"}}`),
	}
	url := "https://example.com/responses"

	httpReq, body, identityState, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, auth, req, req.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}
	applyCodexHeaders(httpReq, auth, "oauth-token", true, executor.cfg)
	applyCodexIdentityConfuseHeaders(httpReq.Header, &identityState)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-1")
	expectedTurnID := codexIdentityConfuseUUID("auth-1", "turn", "turn-1")
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedPromptCacheKey)
	}
	expectedInstallationID := codexIdentityConfuseUUID("auth-1", "installation", "install-1")
	if gotID := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); gotID != expectedInstallationID {
		t.Fatalf("installation id = %q, want %q", gotID, expectedInstallationID)
	}
	gotBodyMetadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	if gotMetadataPromptCacheKey := gjson.Get(gotBodyMetadata, "prompt_cache_key").String(); gotMetadataPromptCacheKey != expectedPromptCacheKey {
		t.Fatalf("client_metadata.x-codex-turn-metadata.prompt_cache_key = %q, want %q", gotMetadataPromptCacheKey, expectedPromptCacheKey)
	}
	if gotMetadataTurnID := gjson.Get(gotBodyMetadata, "turn_id").String(); gotMetadataTurnID != expectedTurnID {
		t.Fatalf("client_metadata.x-codex-turn-metadata.turn_id = %q, want %q", gotMetadataTurnID, expectedTurnID)
	}
	if gotMetadataWindowID := gjson.Get(gotBodyMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("client_metadata.x-codex-turn-metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":0")
	}
	if gotWindowID := gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(); gotWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("client_metadata.x-codex-window-id = %q, want %q", gotWindowID, expectedPromptCacheKey+":0")
	}
	for _, headerName := range []string{"Session-Id", "X-Client-Request-Id", "Thread-Id"} {
		if gotHeader := httpReq.Header.Get(headerName); gotHeader != expectedPromptCacheKey {
			t.Fatalf("%s = %q, want %q", headerName, gotHeader, expectedPromptCacheKey)
		}
	}
	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "" {
		t.Fatalf("Session_id = %q, want empty", gotSession)
	}
	if gotWindow := httpReq.Header.Get("X-Codex-Window-Id"); gotWindow != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", gotWindow, expectedPromptCacheKey+":0")
	}
	gotHeaderMetadata := httpReq.Header.Get("X-Codex-Turn-Metadata")
	if gotMetadataPromptCacheKey := gjson.Get(gotHeaderMetadata, "prompt_cache_key").String(); gotMetadataPromptCacheKey != expectedPromptCacheKey {
		t.Fatalf("X-Codex-Turn-Metadata.prompt_cache_key = %q, want %q", gotMetadataPromptCacheKey, expectedPromptCacheKey)
	}
	if gotMetadataTurnID := gjson.Get(gotHeaderMetadata, "turn_id").String(); gotMetadataTurnID != expectedTurnID {
		t.Fatalf("X-Codex-Turn-Metadata.turn_id = %q, want %q", gotMetadataTurnID, expectedTurnID)
	}
	if gotMetadataWindowID := gjson.Get(gotHeaderMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Turn-Metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":0")
	}
}

func TestCodexIdentityConfuseKeepsClientBodySeparateFromUpstreamBody(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	clientBody := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"cache-1"}`)

	upstreamBody, identityState := applyCodexIdentityConfuseBody(cfg, auth, clientBody, clientBody)
	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-1")
	if identityState.promptCacheKey != expectedPromptCacheKey {
		t.Fatalf("identity prompt_cache_key = %q, want %q", identityState.promptCacheKey, expectedPromptCacheKey)
	}
	if gotKey := gjson.GetBytes(upstreamBody, "prompt_cache_key").String(); gotKey != expectedPromptCacheKey {
		t.Fatalf("upstream prompt_cache_key = %q, want %q", gotKey, expectedPromptCacheKey)
	}
	if gotKey := gjson.GetBytes(clientBody, "prompt_cache_key").String(); gotKey != "cache-1" {
		t.Fatalf("client prompt_cache_key = %q, want cache-1", gotKey)
	}
}

func TestCodexClaudeCacheKeyPreservesLegacyKeyWithoutNamespace(t *testing.T) {
	got := codexClaudeCacheKey("gpt-5-codex", nil, "user-1")
	if got != "gpt-5-codex-user-1" {
		t.Fatalf("codexClaudeCacheKey without namespace = %q, want legacy key", got)
	}
}

func TestCodexClaudeCacheKeyDisambiguatesScopedComponents(t *testing.T) {
	authAB := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a-b"}}
	authA := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a"}}

	keyABUserC := codexClaudeCacheKey("gpt-5-codex", authAB, "c")
	keyAUserBC := codexClaudeCacheKey("gpt-5-codex", authA, "b-c")
	if keyABUserC == "" || keyAUserBC == "" {
		t.Fatalf("codexClaudeCacheKey returned empty scoped key")
	}
	if keyABUserC == "gpt-5-codex-a-b-c" || keyAUserBC == "gpt-5-codex-a-b-c" {
		t.Fatalf("codexClaudeCacheKey used ambiguous delimiter-only key")
	}
	if keyABUserC == keyAUserBC {
		t.Fatalf("codexClaudeCacheKey collided for namespace/user components containing hyphens")
	}

	keyABUserC2 := codexClaudeCacheKey("gpt-5-codex", authAB, "c")
	if keyABUserC2 != keyABUserC {
		t.Fatalf("codexClaudeCacheKey = %q, want stable %q", keyABUserC2, keyABUserC)
	}
}

func TestCodexExecutorCacheHelper_OpenAIResponsesScopesPromptCacheKeyByAuth(t *testing.T) {
	ctx := context.Background()
	executor := &CodexExecutor{}
	rawJSON := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"shared-session"}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: rawJSON,
	}
	url := "https://example.com/responses"
	authA := &cliproxyauth.Auth{
		ID:       "auth-a",
		Metadata: map[string]any{"codex_installation_id": "install-a"},
	}
	authB := &cliproxyauth.Auth{
		ID:       "auth-b",
		Metadata: map[string]any{"codex_installation_id": "install-b"},
	}

	httpReqA, bodyA, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, authA, req, rawJSON, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth A error: %v", err)
	}
	keyA := gjson.GetBytes(bodyA, "prompt_cache_key").String()
	if keyA == "" || keyA == "shared-session" {
		t.Fatalf("auth A prompt_cache_key = %q, want scoped key", keyA)
	}
	if gotSession := httpReqA.Header.Get("Session_id"); gotSession != keyA {
		t.Fatalf("auth A Session_id = %q, want %q", gotSession, keyA)
	}

	_, bodyB, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, authB, req, rawJSON, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth B error: %v", err)
	}
	keyB := gjson.GetBytes(bodyB, "prompt_cache_key").String()
	if keyB == "" || keyB == keyA {
		t.Fatalf("auth B prompt_cache_key = %q, want different from auth A %q", keyB, keyA)
	}

	_, bodyA2, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("openai-response"), url, authA, req, rawJSON, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper auth A second error: %v", err)
	}
	keyA2 := gjson.GetBytes(bodyA2, "prompt_cache_key").String()
	if keyA2 != keyA {
		t.Fatalf("auth A prompt_cache_key second = %q, want stable %q", keyA2, keyA)
	}
}

func TestApplyCodexHeadersPreservesScopedSessionIDOverMacClientHeader(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ginCtx.Request.Header.Set("User-Agent", codexUserAgent)
	ginCtx.Request.Header.Set("Session_id", "client-session")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	httpReq := httptest.NewRequest("POST", "https://example.com/responses", nil).WithContext(ctx)
	httpReq.Header.Set("Session_id", "scoped-session")

	applyCodexHeaders(httpReq, &cliproxyauth.Auth{ID: "auth-a"}, "oauth-token", true, nil)

	if gotSession := httpReq.Header.Get("Session_id"); gotSession != "scoped-session" {
		t.Fatalf("Session_id = %q, want scoped-session", gotSession)
	}
}

func TestScopedCodexPromptCacheKeyDisambiguatesScopedComponents(t *testing.T) {
	authAB := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a:b"}}
	authA := &cliproxyauth.Auth{Metadata: map[string]any{"codex_installation_id": "a"}}

	keyABWithC := scopedCodexPromptCacheKey(authAB, "c")
	keyAWithBC := scopedCodexPromptCacheKey(authA, "b:c")
	if keyABWithC == "" || keyAWithBC == "" {
		t.Fatalf("scopedCodexPromptCacheKey returned empty scoped key")
	}
	if keyABWithC == keyAWithBC {
		t.Fatalf("scopedCodexPromptCacheKey collided for namespace/prompt_cache_key components containing colons")
	}

	ambiguousDelimiterKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:auth-session:a:b:c")).String()
	if keyABWithC == ambiguousDelimiterKey || keyAWithBC == ambiguousDelimiterKey {
		t.Fatalf("scopedCodexPromptCacheKey used ambiguous delimiter-only input")
	}

	if got := scopedCodexPromptCacheKey(nil, " cache-1 "); got != "cache-1" {
		t.Fatalf("scopedCodexPromptCacheKey without namespace = %q, want trimmed legacy key", got)
	}
	if got := scopedCodexPromptCacheKey(authA, "   "); got != "" {
		t.Fatalf("scopedCodexPromptCacheKey blank key = %q, want empty", got)
	}
}
