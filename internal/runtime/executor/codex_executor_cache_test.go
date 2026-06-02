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
	ginCtx.Request.Header.Set("X-Codex-Turn-Metadata", `{"prompt_cache_key":"cache-1","turn_id":"turn-1","parent_thread_id":"parent-1","window_id":"cache-1:2"}`)
	ginCtx.Request.Header.Set("X-Codex-Parent-Thread-Id", "parent-1")
	ginCtx.Request.Header.Set("X-Client-Request-Id", "client-request-1")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	executor := &CodexExecutor{cfg: &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	rawJSON := []byte(`{"model":"gpt-5-codex","stream":true,"client_metadata":{"x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-1\",\"turn_id\":\"turn-1\",\"parent_thread_id\":\"parent-1\",\"window_id\":\"cache-1:2\"}","x-codex-window-id":"cache-1:2","x-codex-parent-thread-id":"parent-1"}}`)
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
	expectedParentThreadID := codexIdentityConfuseUUID("auth-1", "parent-thread", "parent-1")
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedPromptCacheKey)
	}
	if strings.Contains(string(body), "cache-1") {
		t.Fatalf("upstream body still contains original prompt cache key: %s", string(body))
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
	if gotMetadataParentThreadID := gjson.Get(gotBodyMetadata, "parent_thread_id").String(); gotMetadataParentThreadID != expectedParentThreadID {
		t.Fatalf("client_metadata.x-codex-turn-metadata.parent_thread_id = %q, want %q", gotMetadataParentThreadID, expectedParentThreadID)
	}
	if gotMetadataWindowID := gjson.Get(gotBodyMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":2" {
		t.Fatalf("client_metadata.x-codex-turn-metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":2")
	}
	if gotWindowID := gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(); gotWindowID != expectedPromptCacheKey+":2" {
		t.Fatalf("client_metadata.x-codex-window-id = %q, want %q", gotWindowID, expectedPromptCacheKey+":2")
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
	if gotWindow := httpReq.Header.Get("X-Codex-Window-Id"); gotWindow != expectedPromptCacheKey+":2" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", gotWindow, expectedPromptCacheKey+":2")
	}
	if gotParentThreadID := httpReq.Header.Get("X-Codex-Parent-Thread-Id"); gotParentThreadID != expectedParentThreadID {
		t.Fatalf("X-Codex-Parent-Thread-Id = %q, want %q", gotParentThreadID, expectedParentThreadID)
	}
	gotHeaderMetadata := httpReq.Header.Get("X-Codex-Turn-Metadata")
	if gotMetadataPromptCacheKey := gjson.Get(gotHeaderMetadata, "prompt_cache_key").String(); gotMetadataPromptCacheKey != expectedPromptCacheKey {
		t.Fatalf("X-Codex-Turn-Metadata.prompt_cache_key = %q, want %q", gotMetadataPromptCacheKey, expectedPromptCacheKey)
	}
	if gotMetadataTurnID := gjson.Get(gotHeaderMetadata, "turn_id").String(); gotMetadataTurnID != expectedTurnID {
		t.Fatalf("X-Codex-Turn-Metadata.turn_id = %q, want %q", gotMetadataTurnID, expectedTurnID)
	}
	if gotMetadataParentThreadID := gjson.Get(gotHeaderMetadata, "parent_thread_id").String(); gotMetadataParentThreadID != expectedParentThreadID {
		t.Fatalf("X-Codex-Turn-Metadata.parent_thread_id = %q, want %q", gotMetadataParentThreadID, expectedParentThreadID)
	}
	if gotMetadataWindowID := gjson.Get(gotHeaderMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":2" {
		t.Fatalf("X-Codex-Turn-Metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":2")
	}
}

func TestApplyCodexIdentityConfuseBodyDerivesPromptCacheKeyFromMetadata(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	rawJSON := []byte(`{"model":"gpt-5-codex","client_metadata":{"x-codex-turn-metadata":"{\"prompt_cache_key\":\"cache-meta-1\",\"turn_id\":\"turn-meta-1\",\"parent_thread_id\":\"parent-meta-1\",\"window_id\":\"cache-meta-1:6\"}","x-codex-window-id":"cache-meta-1:6","x-codex-parent-thread-id":"parent-meta-1"}}`)

	body, state := applyCodexIdentityConfuseBody(cfg, auth, []byte(`{"model":"gpt-5-codex"}`), rawJSON)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-meta-1")
	expectedTurnID := codexIdentityConfuseUUID("auth-1", "turn", "turn-meta-1")
	if state.promptCacheKey != expectedPromptCacheKey {
		t.Fatalf("state.promptCacheKey = %q, want %q", state.promptCacheKey, expectedPromptCacheKey)
	}
	if strings.Contains(string(body), "cache-meta-1") {
		t.Fatalf("upstream body still contains metadata prompt cache key: %s", string(body))
	}
	if strings.Contains(string(body), "parent-meta-1") {
		t.Fatalf("upstream body still contains parent thread id: %s", string(body))
	}
	gotMetadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	if got := gjson.Get(gotMetadata, "prompt_cache_key").String(); got != expectedPromptCacheKey {
		t.Fatalf("metadata prompt_cache_key = %q, want %q", got, expectedPromptCacheKey)
	}
	if got := gjson.Get(gotMetadata, "turn_id").String(); got != expectedTurnID {
		t.Fatalf("metadata turn_id = %q, want %q", got, expectedTurnID)
	}
	expectedParentThreadID := codexIdentityConfuseUUID("auth-1", "parent-thread", "parent-meta-1")
	if got := gjson.Get(gotMetadata, "parent_thread_id").String(); got != expectedParentThreadID {
		t.Fatalf("metadata parent_thread_id = %q, want %q", got, expectedParentThreadID)
	}
	if got := gjson.Get(gotMetadata, "window_id").String(); got != expectedPromptCacheKey+":6" {
		t.Fatalf("metadata window_id = %q, want %q", got, expectedPromptCacheKey+":6")
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-window-id").String(); got != expectedPromptCacheKey+":6" {
		t.Fatalf("client_metadata.x-codex-window-id = %q, want %q", got, expectedPromptCacheKey+":6")
	}
	if got := gjson.GetBytes(body, "client_metadata.x-codex-parent-thread-id").String(); got != expectedParentThreadID {
		t.Fatalf("parent thread id = %q, want %q", got, expectedParentThreadID)
	}
}

func TestApplyCodexIdentityConfuseBodyConfusesRawJSONInstallationID(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "fill-first"},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "codex"}
	rawJSON := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"cache-1","client_metadata":{"x-codex-installation-id":"install-raw-1"}}`)

	body, _ := applyCodexIdentityConfuseBody(cfg, auth, []byte(`{"model":"gpt-5-codex"}`), rawJSON)

	expectedInstallationID := codexIdentityConfuseUUID("auth-1", "installation", "install-raw-1")
	if got := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); got != expectedInstallationID {
		t.Fatalf("installation id = %q, want %q", got, expectedInstallationID)
	}
	if strings.Contains(string(body), "install-raw-1") {
		t.Fatalf("upstream body still contains raw installation id: %s", string(body))
	}
}

func TestApplyCodexIdentityConfuseHeadersDerivesPromptCacheKeyFromHeaders(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Turn-Metadata", `{"prompt_cache_key":"cache-header-1","turn_id":"turn-header-1","parent_thread_id":"parent-header-1","window_id":"cache-header-1:7"}`)
	headers.Set("X-Codex-Window-Id", "cache-header-1:7")
	headers.Set("X-Codex-Parent-Thread-Id", "parent-header-1")
	headers.Set("Thread-Id", "thread-header-1")
	state := &codexIdentityConfuseState{enabled: true, authID: "auth-1"}

	applyCodexIdentityConfuseHeaders(headers, state)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-header-1")
	expectedTurnID := codexIdentityConfuseUUID("auth-1", "turn", "turn-header-1")
	if strings.Contains(headers.Get("X-Codex-Turn-Metadata"), "cache-header-1") {
		t.Fatalf("upstream turn metadata still contains original prompt cache key: %s", headers.Get("X-Codex-Turn-Metadata"))
	}
	if got := headers.Get("X-Codex-Parent-Thread-Id"); got == "parent-header-1" || got != codexIdentityConfuseUUID("auth-1", "parent-thread", "parent-header-1") {
		t.Fatalf("X-Codex-Parent-Thread-Id = %q, want confused parent thread id", got)
	}
	confusedParentThreadID := headers.Get("X-Codex-Parent-Thread-Id")
	applyCodexIdentityConfuseHeaders(headers, state)
	if got := headers.Get("X-Codex-Parent-Thread-Id"); got != confusedParentThreadID {
		t.Fatalf("X-Codex-Parent-Thread-Id was confused twice: got %q, want %q", got, confusedParentThreadID)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != expectedPromptCacheKey+":7" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", got, expectedPromptCacheKey+":7")
	}
	if got := headers.Get("Thread-Id"); got != expectedPromptCacheKey {
		t.Fatalf("Thread-Id = %q, want %q", got, expectedPromptCacheKey)
	}
	gotMetadata := headers.Get("X-Codex-Turn-Metadata")
	if got := gjson.Get(gotMetadata, "prompt_cache_key").String(); got != expectedPromptCacheKey {
		t.Fatalf("metadata prompt_cache_key = %q, want %q", got, expectedPromptCacheKey)
	}
	if got := gjson.Get(gotMetadata, "turn_id").String(); got != expectedTurnID {
		t.Fatalf("metadata turn_id = %q, want %q", got, expectedTurnID)
	}
	if got := gjson.Get(gotMetadata, "parent_thread_id").String(); got != confusedParentThreadID {
		t.Fatalf("metadata parent_thread_id = %q, want %q", got, confusedParentThreadID)
	}
	if got := gjson.Get(gotMetadata, "window_id").String(); got != expectedPromptCacheKey+":7" {
		t.Fatalf("metadata window_id = %q, want %q", got, expectedPromptCacheKey+":7")
	}
}

func TestApplyCodexIdentityConfuseHeadersDerivesPromptCacheKeyFromWindowHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Window-Id", "cache-window-1:8")
	headers.Set("Thread-Id", "thread-header-1")
	state := &codexIdentityConfuseState{enabled: true, authID: "auth-1"}

	applyCodexIdentityConfuseHeaders(headers, state)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-window-1")
	if got := headers.Get("X-Codex-Window-Id"); got != expectedPromptCacheKey+":8" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", got, expectedPromptCacheKey+":8")
	}
	if got := headers.Get("Thread-Id"); got != expectedPromptCacheKey {
		t.Fatalf("Thread-Id = %q, want %q", got, expectedPromptCacheKey)
	}
}

func TestApplyCodexIdentityConfuseHeadersConfusesParentThreadWithoutPromptCacheKey(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Codex-Parent-Thread-Id", "parent-only-1")
	headers.Set("X-Codex-Turn-Metadata", `{"parent_thread_id":"parent-only-1"}`)
	state := &codexIdentityConfuseState{enabled: true, authID: "auth-1"}

	applyCodexIdentityConfuseHeaders(headers, state)

	expectedParentThreadID := codexIdentityConfuseUUID("auth-1", "parent-thread", "parent-only-1")
	if got := headers.Get("X-Codex-Parent-Thread-Id"); got != expectedParentThreadID {
		t.Fatalf("X-Codex-Parent-Thread-Id = %q, want %q", got, expectedParentThreadID)
	}
	gotMetadata := headers.Get("X-Codex-Turn-Metadata")
	if got := gjson.Get(gotMetadata, "parent_thread_id").String(); got != expectedParentThreadID {
		t.Fatalf("metadata parent_thread_id = %q, want %q", got, expectedParentThreadID)
	}
	if got := headers.Get("Thread-Id"); got != "" {
		t.Fatalf("Thread-Id = %q, want empty without prompt cache key", got)
	}
	if got := headers.Get("X-Codex-Window-Id"); got != "" {
		t.Fatalf("X-Codex-Window-Id = %q, want empty without prompt cache key", got)
	}
}

func TestConfuseCodexWindowIDPreservesOnlyGeneration(t *testing.T) {
	state := &codexIdentityConfuseState{
		enabled:                true,
		authID:                 "auth-1",
		originalPromptCacheKey: "cache-1",
		promptCacheKey:         codexIdentityConfuseUUID("auth-1", "prompt-cache", "cache-1"),
	}

	tests := []struct {
		name     string
		windowID string
		want     string
	}{
		{
			name:     "official original window generation",
			windowID: "cache-1:2",
			want:     state.promptCacheKey + ":2",
		},
		{
			name:     "already confused window generation",
			windowID: state.promptCacheKey + ":3",
			want:     state.promptCacheKey + ":3",
		},
		{
			name:     "multi segment original window keeps only numeric generation",
			windowID: "cache-1:workspace:4",
			want:     state.promptCacheKey + ":4",
		},
		{
			name:     "non numeric original suffix falls back",
			windowID: "cache-1:workspace",
			want:     state.promptCacheKey + ":0",
		},
		{
			name:     "unrelated window with numeric suffix keeps only generation",
			windowID: "other-window:5",
			want:     state.promptCacheKey + ":5",
		},
		{
			name:     "unrelated window without numeric suffix falls back",
			windowID: "other-window",
			want:     state.promptCacheKey + ":0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confuseCodexWindowID(tt.windowID, state); got != tt.want {
				t.Fatalf("confuseCodexWindowID(%q) = %q, want %q", tt.windowID, got, tt.want)
			}
			if strings.Contains(confuseCodexWindowID(tt.windowID, state), "cache-1") {
				t.Fatalf("confused window id leaked original prompt cache key: %q", confuseCodexWindowID(tt.windowID, state))
			}
		})
	}
}

func TestApplyCodexHeadersPreservesWindowAndThreadHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	ginCtx.Request.Header.Set("X-Codex-Window-Id", "cache-1:2")
	ginCtx.Request.Header.Set("Thread-Id", "thread-1")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	httpReq := httptest.NewRequest("POST", "https://example.com/responses", nil).WithContext(ctx)

	applyCodexHeaders(httpReq, &cliproxyauth.Auth{Provider: "codex"}, "oauth-token", true, nil)

	if got := httpReq.Header.Get("X-Codex-Window-Id"); got != "cache-1:2" {
		t.Fatalf("X-Codex-Window-Id = %q, want cache-1:2", got)
	}
	if got := httpReq.Header.Get("Thread-Id"); got != "thread-1" {
		t.Fatalf("Thread-Id = %q, want thread-1", got)
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
