package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestBuildCodexWebsocketRequestBodyPreservesPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body, "")

	if got := gjson.GetBytes(wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %s, want response.create", got)
	}
	if got := gjson.GetBytes(wsReqBody, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %s, want resp-1", got)
	}
	if gjson.GetBytes(wsReqBody, "input.0.id").String() != "msg-1" {
		t.Fatalf("input item id mismatch")
	}
	if got := gjson.GetBytes(wsReqBody, "type").String(); got == "response.append" {
		t.Fatalf("unexpected websocket request type: %s", got)
	}
}

func TestBuildCodexWebsocketRequestBodyIncludesClientMetadata(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body, `{"turn_id":"turn-1","sandbox":"none"}`)

	if got := gjson.GetBytes(wsReqBody, "client_metadata.x-codex-turn-metadata").String(); got != `{"turn_id":"turn-1","sandbox":"none"}` {
		t.Fatalf("client_metadata.x-codex-turn-metadata = %q, want %q", got, `{"turn_id":"turn-1","sandbox":"none"}`)
	}
}

func TestPrepareCodexWebsocketRequestBuildsSharedRequestState(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(&config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent: "config-ua",
		},
	})
	executor.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}

	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Label:    "primary",
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"input":[]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: "openai",
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}

	prepared, err := executor.prepareCodexWebsocketRequest(
		context.Background(),
		auth,
		req,
		opts,
		[]byte(`{"model":"gpt-5-codex","input":[]}`),
		"oauth-token",
		"https://chatgpt.com/backend-api/codex/responses",
	)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequest() error = %v", err)
	}
	defer prepared.unlockSession()

	if got := prepared.wsURL; got != "wss://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("wsURL = %q, want %q", got, "wss://chatgpt.com/backend-api/codex/responses")
	}
	if got := prepared.wsHeaders.Get("Authorization"); got != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer oauth-token")
	}
	if got := prepared.wsHeaders.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %q, want %q", got, "config-ua")
	}
	if got := gjson.GetBytes(prepared.wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %q, want %q", got, "response.create")
	}
	if !bytes.Equal(prepared.wsReqLog.Body, prepared.wsReqBody) {
		t.Fatal("wsReqLog.Body should match wsReqBody")
	}
	if got := prepared.wsReqLog.URL; got != prepared.wsURL {
		t.Fatalf("wsReqLog.URL = %q, want %q", got, prepared.wsURL)
	}
	if got := prepared.authID; got != "auth-1" {
		t.Fatalf("authID = %q, want %q", got, "auth-1")
	}
	if got := prepared.executionSessionID; got != "session-1" {
		t.Fatalf("executionSessionID = %q, want %q", got, "session-1")
	}
	if got := prepared.wsHeaders.Get(codexHeaderSessionID); got != "session-1" {
		t.Fatalf("%s = %q, want %q", codexHeaderSessionID, got, "session-1")
	}
	if got := prepared.wsHeaders.Get(codexHeaderWindowID); got != "session-1:0" {
		t.Fatalf("%s = %q, want %q", codexHeaderWindowID, got, "session-1:0")
	}
	if prepared.sess == nil || prepared.sess.sessionID != "session-1" {
		t.Fatalf("session = %#v, want session-1", prepared.sess)
	}
}

func TestPrepareCodexWebsocketRequestBuildsReusableKeyForPromptCache(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}

	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"prompt_cache_key":"cache-1","input":[]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: "openai-response",
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}

	prepared, err := executor.prepareCodexWebsocketRequest(
		context.Background(),
		auth,
		req,
		opts,
		[]byte(`{"model":"gpt-5-codex","prompt_cache_key":"cache-1","input":[]}`),
		"oauth-token",
		"https://chatgpt.com/backend-api/codex/responses",
	)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequest() error = %v", err)
	}
	defer prepared.unlockSession()

	wantReuseKey := "auth-1|wss://chatgpt.com/backend-api/codex/responses|cache-1"
	if prepared.reuseKey != wantReuseKey {
		t.Fatalf("reuseKey = %q, want %q", prepared.reuseKey, wantReuseKey)
	}
	if prepared.sess == nil {
		t.Fatal("expected session to be created")
	}
	if prepared.sess.reuseKey != wantReuseKey {
		t.Fatalf("session reuseKey = %q, want %q", prepared.sess.reuseKey, wantReuseKey)
	}
}

func TestPrepareCodexWebsocketRequestDerivesPromptCacheForOpenAIChat(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}

	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
	}
	payload := []byte(`{"model":"gpt-5-codex","metadata":{"conversation_id":"conv-42"},"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: payload,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: "openai",
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}

	prepared, err := executor.prepareCodexWebsocketRequest(
		ctxWithAPIKey(t, "api-key-1"),
		auth,
		req,
		opts,
		[]byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`),
		"oauth-token",
		"https://chatgpt.com/backend-api/codex/responses",
	)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequest() error = %v", err)
	}
	defer prepared.unlockSession()

	promptCacheKey := gjson.GetBytes(prepared.wsReqBody, "prompt_cache_key").String()
	if promptCacheKey == "" {
		t.Fatalf("prompt_cache_key should be derived for OpenAI chat websocket request: %s", string(prepared.wsReqBody))
	}
	if got := prepared.wsHeaders.Get("Session_id"); got != promptCacheKey {
		t.Fatalf("Session_id = %q, want prompt_cache_key %q", got, promptCacheKey)
	}
	if !strings.Contains(prepared.reuseKey, "|"+promptCacheKey) {
		t.Fatalf("reuseKey = %q, want to include prompt_cache_key %q", prepared.reuseKey, promptCacheKey)
	}
}

func TestPrepareCodexWebsocketRequestNormalizesFinalUpstreamBody(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}

	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"input":"hello","store":true}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: "openai-response",
	}

	prepared, err := executor.prepareCodexWebsocketRequest(
		context.Background(),
		auth,
		req,
		opts,
		[]byte(`{
			"model":"wrong-model",
			"input":"hello",
			"store":true,
			"stream":false,
			"stream_options":{"include_usage":true},
			"temperature":0.2,
			"context_management":{"compaction":"auto"},
			"previous_response_id":"resp_1"
		}`),
		"oauth-token",
		"https://chatgpt.com/backend-api/codex/responses",
	)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequest() error = %v", err)
	}
	defer prepared.unlockSession()

	body := prepared.body
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5-codex" {
		t.Fatalf("model = %q, want %q", got, "gpt-5-codex")
	}
	if got := gjson.GetBytes(body, "store").Bool(); got {
		t.Fatalf("store = true, want false; body=%s", body)
	}
	if got := gjson.GetBytes(body, "stream").Bool(); !got {
		t.Fatalf("stream = false, want true; body=%s", body)
	}
	if got := gjson.GetBytes(body, "previous_response_id").String(); got != "resp_1" {
		t.Fatalf("previous_response_id = %q, want %q; body=%s", got, "resp_1", body)
	}
	for _, field := range []string{"stream_options", "temperature", "context_management"} {
		if gjson.GetBytes(body, field).Exists() {
			t.Fatalf("%s should be removed from final websocket body: %s", field, body)
		}
	}
	if gjson.GetBytes(body, "instructions").Exists() {
		t.Fatalf("instructions should be omitted when empty: %s", body)
	}
	if got := gjson.GetBytes(body, "tools").IsArray(); !got {
		t.Fatalf("tools should default to an empty array: %s", body)
	}
	if got := gjson.GetBytes(body, "tools.#").Int(); got != 0 {
		t.Fatalf("tools length = %d, want 0; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want %q; body=%s", got, "auto", body)
	}
	if got := gjson.GetBytes(body, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = false, want true; body=%s", body)
	}
	if got := gjson.GetBytes(body, "include").IsArray(); !got {
		t.Fatalf("include should default to an empty array: %s", body)
	}
	if got := gjson.GetBytes(prepared.wsReqBody, "store").Bool(); got {
		t.Fatalf("websocket request body store = true, want false; wsReqBody=%s", prepared.wsReqBody)
	}
	if got := gjson.GetBytes(prepared.wsReqBody, "previous_response_id").String(); got != "resp_1" {
		t.Fatalf("wsReqBody previous_response_id = %q, want %q", got, "resp_1")
	}
}

func TestPrepareCodexWebsocketRequestStripsUnsupportedFinalUpstreamFields(t *testing.T) {
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}

	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: "openai",
	}

	prepared, err := executor.prepareCodexWebsocketRequest(
		context.Background(),
		auth,
		req,
		opts,
		[]byte(`{
			"model":"gpt-5-codex",
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],
			"messages":[{"role":"user","content":"hello"}],
			"metadata":{"conversation_id":"conv-1"},
			"response_format":{"type":"json_schema"},
			"functions":[{"name":"legacy_func"}],
			"trace":{"traceparent":"00-test"}
		}`),
		"oauth-token",
		"https://chatgpt.com/backend-api/codex/responses",
	)
	if err != nil {
		t.Fatalf("prepareCodexWebsocketRequest() error = %v", err)
	}
	defer prepared.unlockSession()

	for _, payload := range [][]byte{prepared.body, prepared.wsReqBody} {
		for _, field := range []string{"messages", "metadata", "response_format", "functions", "trace"} {
			if gjson.GetBytes(payload, field).Exists() {
				t.Fatalf("%s should not reach websocket Codex upstream payload: %s", field, payload)
			}
		}
		if got := gjson.GetBytes(payload, "input.0.content.0.text").String(); got != "hello" {
			t.Fatalf("input.0.content.0.text = %q, want %q; payload=%s", got, "hello", payload)
		}
	}
}

func TestApplyCodexWebsocketHeadersDefaultsToCurrentResponsesBeta(t *testing.T) {
	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, nil, "", nil)

	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
	if got := headers.Get("User-Agent"); got != misc.CodexCLIUserAgent {
		t.Fatalf("User-Agent = %s, want %s", got, misc.CodexCLIUserAgent)
	}
	if got := headers.Get("Version"); got != buildinfo.Version {
		t.Fatalf("Version = %q, want %q", got, buildinfo.Version)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	assertGeneratedCodexTurnMetadata(t, headers.Get("X-Codex-Turn-Metadata"))
	if got := headers.Get("Session_id"); got == "" {
		t.Fatal("Session_id should be generated by default")
	}
	if got := headers.Get("X-Client-Request-Id"); got != headers.Get("Session_id") {
		t.Fatalf("X-Client-Request-Id = %q, want Session_id %q", got, headers.Get("Session_id"))
	}
}

func TestApplyCodexWebsocketHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := headers.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexWebsocketHeadersUsesDerivedSessionHeadersWithoutForwardingConversationID(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:Originator": "codex_vscode",
		},
	}
	headers := http.Header{
		"Conversation_id": []string{"conv-1"},
	}

	got := applyCodexWebsocketHeaders(context.Background(), headers, auth, "", nil)

	if gotConversation := got.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := got.Get("Session_id"); gotSession != "conv-1" {
		t.Fatalf("Session_id = %q, want %q", gotSession, "conv-1")
	}
	if gotRequestID := got.Get("X-Client-Request-Id"); gotRequestID != "conv-1" {
		t.Fatalf("X-Client-Request-Id = %q, want %q", gotRequestID, "conv-1")
	}
	if gotOriginator := got.Get("Originator"); gotOriginator != "codex_vscode" {
		t.Fatalf("Originator = %q, want %q", gotOriginator, "codex_vscode")
	}
	if gotUA := got.Get("User-Agent"); !strings.HasPrefix(gotUA, "codex_vscode/") {
		t.Fatalf("User-Agent = %q, want codex_vscode/ prefix", gotUA)
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigDefaultsForOAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "my-codex-client/1.0",
			BetaFeatures: "feature-a,feature-b",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "my-codex-client/1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "my-codex-client/1.0")
	}
	if got := headers.Get("x-codex-beta-features"); got != "feature-a,feature-b" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "feature-a,feature-b")
	}
	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesExistingHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	headers.Set("X-Codex-Beta-Features", "existing-beta")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", gotVal, "config-ua")
	}
	if gotVal := got.Get("x-codex-beta-features"); gotVal != "existing-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", gotVal, "existing-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesClientHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := headers.Get("x-codex-beta-features"); got != "client-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "client-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesAuthFileAndClient(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"email":      "user@example.com",
			"user_agent": "auth-file-ua",
		},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", gotVal, "config-ua")
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigUserAgentForAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "sk-test", cfg)

	if got := headers.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := headers.Get("x-codex-beta-features"); got != "config-beta" {
		t.Fatalf("x-codex-beta-features = %q, want config-beta", got)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %s, want %s", got, codexOriginator)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForOAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "config-beta" {
		t.Fatalf("x-codex-beta-features = %q, want config-beta", got)
	}
}

func TestApplyCodexHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if got := req.Header.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := req.Header.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexHeadersUsesDerivedSessionHeadersWithoutForwardingConversationID(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"header:Originator": "codex_vscode",
		},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Conversation_id": "conv-1",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if gotConversation := req.Header.Get("Conversation_id"); gotConversation != "" {
		t.Fatalf("Conversation_id = %q, want empty", gotConversation)
	}
	if gotSession := req.Header.Get("Session_id"); gotSession != "conv-1" {
		t.Fatalf("Session_id = %q, want %q", gotSession, "conv-1")
	}
	if gotRequestID := req.Header.Get("X-Client-Request-Id"); gotRequestID != "conv-1" {
		t.Fatalf("X-Client-Request-Id = %q, want %q", gotRequestID, "conv-1")
	}
	if gotOriginator := req.Header.Get("Originator"); gotOriginator != "codex_vscode" {
		t.Fatalf("Originator = %q, want %q", gotOriginator, "codex_vscode")
	}
	if gotUA := req.Header.Get("User-Agent"); !strings.HasPrefix(gotUA, "codex_vscode/") {
		t.Fatalf("User-Agent = %q, want codex_vscode/ prefix", gotUA)
	}
}

func TestApplyCodexHeadersConfigUserAgentOverridesAuthFileAndClient(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "existing-ua")

	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
		Attributes: map[string]string{
			"header:User-Agent": "auth-file-ua",
		},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "config-beta" {
		t.Fatalf("x-codex-beta-features = %q, want config-beta", got)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForAPIKeyAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	}

	applyCodexHeaders(req, auth, "sk-test", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "config-beta" {
		t.Fatalf("x-codex-beta-features = %q, want config-beta", got)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	if got := req.Header.Get("Version"); got != buildinfo.Version {
		t.Fatalf("Version = %q, want %q", got, buildinfo.Version)
	}
	if got := req.Header.Get("Connection"); got != "" {
		t.Fatalf("Connection = %q, want empty", got)
	}
}

func TestApplyCodexHeadersDoesNotInjectClientOnlyHeadersByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get("User-Agent"); got != misc.CodexCLIUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	if got := req.Header.Get("Version"); got != buildinfo.Version {
		t.Fatalf("Version = %q, want %q", got, buildinfo.Version)
	}
	assertGeneratedCodexTurnMetadata(t, req.Header.Get("X-Codex-Turn-Metadata"))
	if got := req.Header.Get("Session_id"); got == "" {
		t.Fatal("Session_id should be generated by default")
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != req.Header.Get("Session_id") {
		t.Fatalf("X-Client-Request-Id = %q, want Session_id %q", got, req.Header.Get("Session_id"))
	}
}

func TestApplyCodexHeadersCompactKeepsHeadersLeanByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses/compact", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", false, nil)

	if got := req.Header.Get("User-Agent"); got != misc.CodexCLIUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got := req.Header.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %q, want %q", got, codexOriginator)
	}
	if got := req.Header.Get("Version"); got != buildinfo.Version {
		t.Fatalf("Version = %q, want %q", got, buildinfo.Version)
	}
	if got := req.Header.Get(codexHeaderSessionID); got == "" {
		t.Fatal("Session_id should be generated for compact requests")
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
	if got := req.Header.Get(codexHeaderTurnMetadata); got != "" {
		t.Fatalf("%s = %q, want empty", codexHeaderTurnMetadata, got)
	}
	if got := req.Header.Get(codexHeaderTurnState); got != "" {
		t.Fatalf("%s = %q, want empty", codexHeaderTurnState, got)
	}
}

func TestApplyCodexHeadersCompactPreservesExplicitTurnHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses/compact", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Codex-Turn-State":    "turn-state-1",
		"X-Client-Request-Id":   "request-1",
	}))

	applyCodexHeaders(req, nil, "oauth-token", false, nil)

	if got := req.Header.Get(codexHeaderTurnMetadata); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("%s = %q, want explicit value", codexHeaderTurnMetadata, got)
	}
	if got := req.Header.Get(codexHeaderTurnState); got != "turn-state-1" {
		t.Fatalf("%s = %q, want explicit value", codexHeaderTurnState, got)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "request-1" {
		t.Fatalf("X-Client-Request-Id = %q, want explicit value", got)
	}
}

func TestApplyCodexHeadersUsesTurnMetadataSessionIDWhenMissing(t *testing.T) {
	resetCodexWindowStateStore()

	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		codexHeaderTurnMetadata: `{"session_id":"turn-session-1","turn_id":"turn-1","sandbox":"none"}`,
	}))

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get(codexHeaderSessionID); got != "turn-session-1" {
		t.Fatalf("%s = %q, want %q", codexHeaderSessionID, got, "turn-session-1")
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "turn-session-1" {
		t.Fatalf("X-Client-Request-Id = %q, want %q", got, "turn-session-1")
	}
	if got := req.Header.Get(codexHeaderWindowID); got != "turn-session-1:0" {
		t.Fatalf("%s = %q, want %q", codexHeaderWindowID, got, "turn-session-1:0")
	}
	if got := req.Header.Get(codexHeaderTurnMetadata); got != `{"session_id":"turn-session-1","turn_id":"turn-1","sandbox":"none"}` {
		t.Fatalf("%s = %q, want explicit value", codexHeaderTurnMetadata, got)
	}
}

func TestEnsureUpstreamConnRedialsRecentlyActiveBrokenConnection(t *testing.T) {
	var (
		upgrader    = websocket.Upgrader{}
		accepted    atomic.Int32
		serverMu    sync.Mutex
		serverConns []*websocket.Conn
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		accepted.Add(1)
		serverMu.Lock()
		serverConns = append(serverConns, conn)
		serverMu.Unlock()
	}))
	defer server.Close()
	defer func() {
		serverMu.Lock()
		defer serverMu.Unlock()
		for _, conn := range serverConns {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	staleConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if err := staleConn.Close(); err != nil {
		t.Fatalf("Close() stale conn error = %v", err)
	}

	executor := NewCodexWebsocketsExecutor(nil)
	sess := &codexWebsocketSession{sessionID: "session-redial"}
	sess.conn = staleConn
	sess.readerConn = staleConn
	sess.lastActivityUnixNano.Store(time.Now().UnixNano())
	defer closeCodexWebsocketSession(sess, "test_cleanup")

	conn, _, err := executor.ensureUpstreamConn(context.Background(), nil, sess, "auth-1", wsURL, http.Header{})
	if err != nil {
		t.Fatalf("ensureUpstreamConn() error = %v", err)
	}
	if conn == nil {
		t.Fatal("ensureUpstreamConn() returned nil conn")
	}
	if conn == staleConn {
		t.Fatal("ensureUpstreamConn() should redial instead of reusing a broken recent conn")
	}
	if got := accepted.Load(); got != 2 {
		t.Fatalf("accepted connections = %d, want 2", got)
	}
	if sess.conn != conn {
		t.Fatal("session should store the redialed conn")
	}
}

func TestCloseExecutionSessionParksReusableSessionAndReattaches(t *testing.T) {
	oldTTL := codexResponsesWebsocketParkTTL
	codexResponsesWebsocketParkTTL = 5 * time.Second
	defer func() {
		codexResponsesWebsocketParkTTL = oldTTL
	}()

	var (
		upgrader    = websocket.Upgrader{}
		accepted    atomic.Int32
		serverMu    sync.Mutex
		serverConns []*websocket.Conn
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		accepted.Add(1)
		serverMu.Lock()
		serverConns = append(serverConns, conn)
		serverMu.Unlock()
	}))
	defer server.Close()
	defer func() {
		serverMu.Lock()
		defer serverMu.Unlock()
		for _, conn := range serverConns {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}

	store := &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = store

	reuseKey := "auth-1|" + wsURL + "|cache-1"
	sess1 := executor.getOrCreateSession("exec-1", reuseKey)
	if sess1 == nil {
		t.Fatal("expected session to be created")
	}
	sess1.conn = conn
	sess1.readerConn = conn
	sess1.wsURL = wsURL
	sess1.authID = "auth-1"
	sess1.touchActivity()

	executor.CloseExecutionSession("exec-1")

	store.mu.Lock()
	parked := store.parked[reuseKey]
	store.mu.Unlock()
	if parked != sess1 {
		t.Fatal("expected session to be parked for reuse")
	}

	sess2 := executor.getOrCreateSession("exec-2", reuseKey)
	if sess2 != sess1 {
		t.Fatal("expected parked session to be reattached")
	}
	if sess2.sessionID != "exec-2" {
		t.Fatalf("sessionID = %q, want exec-2", sess2.sessionID)
	}

	reusedConn, _, err := executor.ensureUpstreamConn(context.Background(), nil, sess2, "auth-1", wsURL, http.Header{})
	if err != nil {
		t.Fatalf("ensureUpstreamConn() error = %v", err)
	}
	if reusedConn != conn {
		t.Fatal("expected parked session to reuse original upstream conn")
	}
	if got := accepted.Load(); got != 1 {
		t.Fatalf("accepted connections = %d, want 1", got)
	}

	executor.closeAllExecutionSessions("test_cleanup")
}

func TestResetExecutionSessionClosesReusableSessionWithoutParking(t *testing.T) {
	oldTTL := codexResponsesWebsocketParkTTL
	codexResponsesWebsocketParkTTL = 5 * time.Second
	defer func() {
		codexResponsesWebsocketParkTTL = oldTTL
	}()

	var (
		upgrader    = websocket.Upgrader{}
		accepted    atomic.Int32
		serverMu    sync.Mutex
		serverConns []*websocket.Conn
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		accepted.Add(1)
		serverMu.Lock()
		serverConns = append(serverConns, conn)
		serverMu.Unlock()
	}))
	defer server.Close()
	defer func() {
		serverMu.Lock()
		defer serverMu.Unlock()
		for _, conn := range serverConns {
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}

	store := &codexWebsocketSessionStore{
		sessions: make(map[string]*codexWebsocketSession),
		parked:   make(map[string]*codexWebsocketSession),
	}
	executor := NewCodexWebsocketsExecutor(nil)
	executor.store = store

	reuseKey := "auth-1|" + wsURL + "|cache-1"
	sess1 := executor.getOrCreateSession("exec-1", reuseKey)
	if sess1 == nil {
		t.Fatal("expected session to be created")
	}
	sess1.conn = conn
	sess1.readerConn = conn
	sess1.wsURL = wsURL
	sess1.authID = "auth-1"
	sess1.touchActivity()

	executor.ResetExecutionSession("exec-1")

	store.mu.Lock()
	_, active := store.sessions["exec-1"]
	parked := store.parked[reuseKey]
	store.mu.Unlock()
	if active {
		t.Fatal("expected active session to be removed after reset")
	}
	if parked != nil {
		t.Fatal("expected reset session not to be parked")
	}

	sess2 := executor.getOrCreateSession("exec-2", reuseKey)
	if sess2 == sess1 {
		t.Fatal("expected reset to force a fresh session")
	}

	reconnected, _, err := executor.ensureUpstreamConn(context.Background(), nil, sess2, "auth-1", wsURL, http.Header{})
	if err != nil {
		t.Fatalf("ensureUpstreamConn() error = %v", err)
	}
	if reconnected == conn {
		t.Fatal("expected reset session to dial a fresh upstream conn")
	}
	if got := accepted.Load(); got != 2 {
		t.Fatalf("accepted connections = %d, want 2", got)
	}

	executor.closeAllExecutionSessions("test_cleanup")
}

func TestCodexWebsocketsExecuteStreamTranslatesAndNormalizesOpenAIResponsesRequest(t *testing.T) {
	var (
		upgrader = websocket.Upgrader{}
		received = make(chan []byte, 1)
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("ReadMessage() error = %v", err)
			return
		}
		received <- append([]byte(nil), payload...)

		if err := conn.WriteJSON(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_1",
				"object": "response",
				"status": "completed",
				"output": []any{},
				"usage": map[string]any{
					"input_tokens":  1,
					"output_tokens": 0,
					"total_tokens":  1,
				},
			},
		}); err != nil {
			t.Errorf("WriteJSON() error = %v", err)
		}
	}))
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello","store":true,"stream":true}`),
	}
	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai-response"),
		OriginalRequest: req.Payload,
	}

	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	requestBody := <-received
	if got := gjson.GetBytes(requestBody, "type").String(); got != "response.create" {
		t.Fatalf("websocket type = %q, want response.create; body=%s", got, requestBody)
	}
	if got := gjson.GetBytes(requestBody, "store").Bool(); got {
		t.Fatalf("websocket store = true, want false; body=%s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "stream").Bool(); !got {
		t.Fatalf("websocket stream = false, want true; body=%s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input").IsArray(); !got {
		t.Fatalf("input should be translated to an array; body=%s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want %q; body=%s", got, "message", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input.0.content.0.text").String(); got != "hello" {
		t.Fatalf("input.0.content.0.text = %q, want %q; body=%s", got, "hello", requestBody)
	}
	if gjson.GetBytes(requestBody, "messages").Exists() {
		t.Fatalf("messages should not be forwarded to websocket upstream: %s", requestBody)
	}

	for range result.Chunks {
	}
}

func TestCodexWebsocketsExecuteStreamTranslatesAndNormalizesOpenAIChatRequest(t *testing.T) {
	var (
		upgrader = websocket.Upgrader{}
		received = make(chan []byte, 1)
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("Upgrade() error = %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("ReadMessage() error = %v", err)
			return
		}
		received <- append([]byte(nil), payload...)

		if err := conn.WriteJSON(map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":     "resp_1",
				"object": "response",
				"status": "completed",
				"output": []any{},
				"usage": map[string]any{
					"input_tokens":  1,
					"output_tokens": 0,
					"total_tokens":  1,
				},
			},
		}); err != nil {
			t.Errorf("WriteJSON() error = %v", err)
		}
	}))
	defer server.Close()

	executor := NewCodexWebsocketsExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"messages":[{"role":"user","content":"hello"}],
			"metadata":{"conversation_id":"conv-1"},
			"store":true
		}`),
	}
	opts := cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: req.Payload,
	}

	result, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	requestBody := <-received
	if got := gjson.GetBytes(requestBody, "type").String(); got != "response.create" {
		t.Fatalf("websocket type = %q, want response.create; body=%s", got, requestBody)
	}
	if got := gjson.GetBytes(requestBody, "store").Bool(); got {
		t.Fatalf("websocket store = true, want false; body=%s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "stream").Bool(); !got {
		t.Fatalf("websocket stream = false, want true; body=%s", requestBody)
	}
	if gjson.GetBytes(requestBody, "messages").Exists() {
		t.Fatalf("messages should not be forwarded to websocket upstream: %s", requestBody)
	}
	if gjson.GetBytes(requestBody, "metadata").Exists() {
		t.Fatalf("metadata should not be forwarded to websocket upstream: %s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input").IsArray(); !got {
		t.Fatalf("input should be translated to an array; body=%s", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input.0.type").String(); got != "message" {
		t.Fatalf("input.0.type = %q, want %q; body=%s", got, "message", requestBody)
	}
	if got := gjson.GetBytes(requestBody, "input.0.content.0.text").String(); got != "hello" {
		t.Fatalf("input.0.content.0.text = %q, want %q; body=%s", got, "hello", requestBody)
	}

	for range result.Chunks {
	}
}

func contextWithGinHeaders(headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ginCtx.Request.Header = make(http.Header, len(headers))
	for key, value := range headers {
		ginCtx.Request.Header.Set(key, value)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func assertGeneratedCodexTurnMetadata(t *testing.T, raw string) {
	t.Helper()

	if strings.TrimSpace(raw) == "" {
		t.Fatal("X-Codex-Turn-Metadata should be generated by default")
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatalf("X-Codex-Turn-Metadata should be valid JSON: %v", err)
	}

	turnID, _ := metadata["turn_id"].(string)
	if strings.TrimSpace(turnID) == "" {
		t.Fatalf("turn_id = %q, want non-empty", turnID)
	}
	sessionID, _ := metadata["session_id"].(string)
	if strings.TrimSpace(sessionID) == "" {
		t.Fatalf("session_id = %q, want non-empty", sessionID)
	}
	if threadSource, _ := metadata["thread_source"].(string); threadSource != codexDefaultThreadSource {
		t.Fatalf("thread_source = %q, want %q", threadSource, codexDefaultThreadSource)
	}
	if sandbox, _ := metadata["sandbox"].(string); sandbox != codexDefaultSandboxTag {
		t.Fatalf("sandbox = %q, want %q", sandbox, codexDefaultSandboxTag)
	}
}

func TestNewProxyAwareWebsocketDialerDirectDisablesProxy(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
	)

	if dialer.Proxy != nil {
		t.Fatal("expected websocket proxy function to be nil for direct mode")
	}
}
