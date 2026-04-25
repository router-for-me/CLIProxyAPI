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
