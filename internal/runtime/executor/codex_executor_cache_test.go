package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
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
	if gotSession := httpReq.Header["Session_id"]; len(gotSession) != 1 || gotSession[0] != expectedKey {
		t.Fatalf("Session_id = %#v, want [%q]", gotSession, expectedKey)
	}
	if gotCanonicalSession := httpReq.Header.Get("Session-Id"); gotCanonicalSession != "" {
		t.Fatalf("Session-Id = %q, want empty", gotCanonicalSession)
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

func TestCodexExecutorCacheHelper_ClaudeUsesClaudeCodeSessionID(t *testing.T) {
	executor := &CodexExecutor{}
	ctx := context.Background()
	url := "https://example.com/responses"
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)
	firstReq := cliproxyexecutor.Request{
		Model: "gpt-5.4-claude-cache-session",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-a\",\"account_uuid\":\"\",\"session_id\":\"cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]
		}`),
	}
	secondReq := cliproxyexecutor.Request{
		Model: "gpt-5.4-claude-cache-session",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"metadata":{"user_id":"{\"device_id\":\"device-b\",\"account_uuid\":\"\",\"session_id\":\"cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]
		}`),
	}

	firstHTTPReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("claude"), url, nil, firstReq, firstReq.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper first error: %v", err)
	}
	secondHTTPReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("claude"), url, nil, secondReq, secondReq.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper second error: %v", err)
	}

	firstBody, errRead := io.ReadAll(firstHTTPReq.Body)
	if errRead != nil {
		t.Fatalf("read first request body: %v", errRead)
	}
	secondBody, errRead := io.ReadAll(secondHTTPReq.Body)
	if errRead != nil {
		t.Fatalf("read second request body: %v", errRead)
	}
	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" {
		t.Fatalf("first prompt_cache_key is empty; body=%s", string(firstBody))
	}
	if secondKey != firstKey {
		t.Fatalf("same Claude Code session_id produced different prompt_cache_key: first=%q second=%q", firstKey, secondKey)
	}
	if gotSession := firstHTTPReq.Header["Session_id"]; len(gotSession) != 1 || gotSession[0] != firstKey {
		t.Fatalf("first Session_id = %#v, want [%q]", gotSession, firstKey)
	}
	if gotSession := secondHTTPReq.Header["Session_id"]; len(gotSession) != 1 || gotSession[0] != firstKey {
		t.Fatalf("second Session_id = %#v, want [%q]", gotSession, firstKey)
	}
}

func TestCodexExecutorCacheHelper_ClaudeRejectsBareUserID(t *testing.T) {
	executor := &CodexExecutor{}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4-claude-cache-bare-user",
		Payload: []byte(`{"model":"gpt-5.4","metadata":{"user_id":"same-user-across-chats"},"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]}`),
	}

	httpReq, _, _, err := executor.cacheHelper(context.Background(), sdktranslator.FromString("claude"), "https://example.com/responses", nil, req, req.Payload, []byte(`{"model":"gpt-5.4","stream":true}`))
	if err != nil {
		t.Fatalf("cacheHelper error: %v", err)
	}

	body, errRead := io.ReadAll(httpReq.Body)
	if errRead != nil {
		t.Fatalf("read request body: %v", errRead)
	}
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "" {
		t.Fatalf("bare metadata.user_id must not create prompt_cache_key, got %q; body=%s", got, string(body))
	}
	if got := httpReq.Header["Session_id"]; len(got) != 0 {
		t.Fatalf("bare metadata.user_id must not create Session_id, got %#v", got)
	}
	if got := httpReq.Header.Get("Session-Id"); got != "" {
		t.Fatalf("bare metadata.user_id must not create Session-Id, got %q", got)
	}
}

func TestCodexExecutorCacheHelper_IdentityConfuseRemapsBodyAndHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
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
	if gotHeader := httpReq.Header["Session_id"]; len(gotHeader) != 1 || gotHeader[0] != expectedPromptCacheKey {
		t.Fatalf("Session_id = %#v, want [%q]", gotHeader, expectedPromptCacheKey)
	}
	for _, headerName := range []string{"X-Client-Request-Id", "Thread-Id"} {
		if gotHeader := httpReq.Header.Get(headerName); gotHeader != expectedPromptCacheKey {
			t.Fatalf("%s = %q, want %q", headerName, gotHeader, expectedPromptCacheKey)
		}
	}
	if gotCanonicalSession := httpReq.Header.Get("Session-Id"); gotCanonicalSession != "" {
		t.Fatalf("Session-Id = %q, want empty", gotCanonicalSession)
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

func TestApplyCodexHeadersUsesAccountHeaderForOAuth(t *testing.T) {
	httpReq := httptest.NewRequest("POST", "https://example.com/responses", nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"account_id": "acct-1"},
	}

	applyCodexHeaders(httpReq, auth, "oauth-token", true, nil)

	if got := httpReq.Header.Get("Chatgpt-Account-Id"); got != "acct-1" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-1", got)
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

func TestCodexExecutorCacheHelper_ClaudeUsesSessionHeader(t *testing.T) {
	executor := &CodexExecutor{}
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ginCtx.Request.Header.Set(helps.ClaudeCodeSessionHeader, "cache-session-header")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	firstReq := cliproxyexecutor.Request{
		Model:   "gpt-5.4-claude-cache-header",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]}`),
	}
	secondReq := cliproxyexecutor.Request{
		Model:   "gpt-5.4-claude-cache-header",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]}`),
	}
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)
	url := "https://example.com/responses"

	firstHTTPReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("claude"), url, nil, firstReq, firstReq.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper first error: %v", err)
	}
	secondHTTPReq, _, _, err := executor.cacheHelper(ctx, sdktranslator.FromString("claude"), url, nil, secondReq, secondReq.Payload, rawJSON)
	if err != nil {
		t.Fatalf("cacheHelper second error: %v", err)
	}

	firstBody, errRead := io.ReadAll(firstHTTPReq.Body)
	if errRead != nil {
		t.Fatalf("read first request body: %v", errRead)
	}
	secondBody, errRead := io.ReadAll(secondHTTPReq.Body)
	if errRead != nil {
		t.Fatalf("read second request body: %v", errRead)
	}
	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" {
		t.Fatalf("first prompt_cache_key is empty; body=%s", string(firstBody))
	}
	if secondKey != firstKey {
		t.Fatalf("same Claude Code session header produced different prompt_cache_key: first=%q second=%q", firstKey, secondKey)
	}
}

func TestCodexExecutorCacheHelper_ClaudeAgentScopeUsesResolvedModelAcrossHTTPAndWebsocket(t *testing.T) {
	executor := &CodexExecutor{}
	url := "https://example.com/responses"
	req := cliproxyexecutor.Request{
		Model:   "requested-alias-high",
		Payload: []byte(`{"model":"requested-alias","messages":[{"role":"user","content":"hello"}]}`),
	}
	rootHeaders := http.Header{}
	rootHeaders.Set(helps.ClaudeCodeSessionHeader, "resolved-model-session")
	childHeaders := rootHeaders.Clone()
	childHeaders.Set(helps.ClaudeCodeAgentHeader, "agent-a")
	rawJSON := []byte(`{"model":"gpt-5.4","stream":true}`)

	rootRequest, _, _, errRoot := executor.cacheHelper(context.Background(), sdktranslator.FromString("claude"), url, nil, req, req.Payload, rawJSON, rootHeaders)
	if errRoot != nil {
		t.Fatalf("root cacheHelper error: %v", errRoot)
	}
	rootBody, errReadRoot := io.ReadAll(rootRequest.Body)
	if errReadRoot != nil {
		t.Fatalf("read root body: %v", errReadRoot)
	}
	rootKey := gjson.GetBytes(rootBody, "prompt_cache_key").String()

	childRequest, _, _, errChild := executor.cacheHelper(context.Background(), sdktranslator.FromString("claude"), url, nil, req, req.Payload, rawJSON, childHeaders)
	if errChild != nil {
		t.Fatalf("child cacheHelper error: %v", errChild)
	}
	childBody, errReadChild := io.ReadAll(childRequest.Body)
	if errReadChild != nil {
		t.Fatalf("read child body: %v", errReadChild)
	}
	childKey := gjson.GetBytes(childBody, "prompt_cache_key").String()
	if rootKey == "" || childKey == "" || rootKey == childKey {
		t.Fatalf("agent prompt keys are not isolated: root=%q child=%q", rootKey, childKey)
	}

	aliasReq := req
	aliasReq.Model = "another-local-alias-low"
	aliasRequest, _, _, errAlias := executor.cacheHelper(context.Background(), sdktranslator.FromString("claude"), url, nil, aliasReq, aliasReq.Payload, rawJSON, childHeaders)
	if errAlias != nil {
		t.Fatalf("alias cacheHelper error: %v", errAlias)
	}
	aliasBody, errReadAlias := io.ReadAll(aliasRequest.Body)
	if errReadAlias != nil {
		t.Fatalf("read alias body: %v", errReadAlias)
	}
	if aliasKey := gjson.GetBytes(aliasBody, "prompt_cache_key").String(); aliasKey != childKey {
		t.Fatalf("resolved model key fragmented by request alias: first=%q alias=%q", childKey, aliasKey)
	}

	websocketBody, _, errWebsocket := applyCodexPromptCacheHeadersWithContext(context.Background(), sdktranslator.FromString("claude"), aliasReq, rawJSON, childHeaders)
	if errWebsocket != nil {
		t.Fatalf("websocket prompt cache error: %v", errWebsocket)
	}
	if websocketKey := gjson.GetBytes(websocketBody, "prompt_cache_key").String(); websocketKey != childKey {
		t.Fatalf("HTTP/WebSocket prompt keys differ: http=%q websocket=%q", childKey, websocketKey)
	}
}
