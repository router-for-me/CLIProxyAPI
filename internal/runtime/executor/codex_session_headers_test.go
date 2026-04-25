package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexSessionKey_EmptyWhenNoCacheID(t *testing.T) {
	if key := codexSessionKey(&cliproxyauth.Auth{ID: "a"}, ""); key != "" {
		t.Fatalf("codexSessionKey must be empty when promptCacheID is blank, got %q", key)
	}
	if key := codexSessionKey(nil, "   "); key != "" {
		t.Fatalf("codexSessionKey must trim whitespace, got %q", key)
	}
}

func TestCodexSessionKey_AnonymousVsIdentified(t *testing.T) {
	anon := codexSessionKey(nil, "pc-1")
	if anon != "anon|pc-1" {
		t.Fatalf("anonymous key = %q, want anon|pc-1", anon)
	}
	ided := codexSessionKey(&cliproxyauth.Auth{ID: "auth-42"}, "pc-1")
	if ided != "auth-42|pc-1" {
		t.Fatalf("identified key = %q, want auth-42|pc-1", ided)
	}
	if anon == ided {
		t.Fatalf("anonymous and identified callers must map to distinct keys")
	}
}

func TestInjectCodexSessionHeaders_UsesCacheWhenTargetEmpty(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	key := "auth-42|pc-1"
	helps.UpdateCodexSession(key, func(s *helps.CodexSessionState) {
		s.SessionID = "sess-stable"
		s.TurnState = "turn-token"
		s.TurnMetadata = `{"turn_id":"t-1"}`
	})

	headers := http.Header{}
	if !injectCodexSessionHeaders(headers, key) {
		t.Fatalf("expected injection to report populated=true")
	}
	if got := headers.Get(codexHeaderSessionID); got != "sess-stable" {
		t.Fatalf("Session_id = %q, want sess-stable", got)
	}
	if got := headers.Get(codexHeaderTurnState); got != "turn-token" {
		t.Fatalf("turn-state = %q, want turn-token", got)
	}
	if got := headers.Get(codexHeaderTurnMetadata); got != `{"turn_id":"t-1"}` {
		t.Fatalf("turn-metadata = %q, want %q", got, `{"turn_id":"t-1"}`)
	}
}

func TestInjectCodexSessionHeaders_NeverOverridesCaller(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	key := "auth-42|pc-2"
	helps.UpdateCodexSession(key, func(s *helps.CodexSessionState) {
		s.TurnState = "cached-token"
	})

	headers := http.Header{}
	headers.Set(codexHeaderTurnState, "client-token")

	_ = injectCodexSessionHeaders(headers, key)
	if got := headers.Get(codexHeaderTurnState); got != "client-token" {
		t.Fatalf("client-provided turn-state must win, got %q", got)
	}
}

func TestCaptureCodexSessionHeaders_PersistsTurnStateAndMetadata(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	key := "auth-42|pc-3"
	resp := http.Header{}
	resp.Set(codexHeaderTurnState, "captured-token")
	resp.Set(codexHeaderTurnMetadata, `{"turn_id":"t-9"}`)

	captureCodexSessionHeaders(key, "sess-stable", resp)

	state, ok := helps.GetCodexSession(key)
	if !ok {
		t.Fatalf("expected cached session for key %q", key)
	}
	if state.SessionID != "sess-stable" {
		t.Fatalf("SessionID = %q, want sess-stable", state.SessionID)
	}
	if state.TurnState != "captured-token" {
		t.Fatalf("TurnState = %q, want captured-token", state.TurnState)
	}
	if state.TurnMetadata != `{"turn_id":"t-9"}` {
		t.Fatalf("TurnMetadata = %q, want %q", state.TurnMetadata, `{"turn_id":"t-9"}`)
	}
}

func TestCaptureCodexSessionHeaders_NoopWhenNothingToCapture(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	captureCodexSessionHeaders("auth-42|pc-4", "", http.Header{})

	if _, ok := helps.GetCodexSession("auth-42|pc-4"); ok {
		t.Fatalf("no entry should be created when there is nothing to store")
	}
}

func TestCaptureCodexWebsocketSessionHeaders_PersistsFromHandshake(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	auth := &cliproxyauth.Auth{ID: "auth-ws"}
	reqHeaders := http.Header{}
	// applyCodexPromptCacheHeaders sets Conversation_id; simulate that.
	reqHeaders.Set("Conversation_id", "pc-ws-1")
	reqHeaders.Set(codexHeaderSessionID, "sess-ws-stable")

	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set(codexHeaderTurnState, "ws-captured-token")
	resp.Header.Set(codexHeaderTurnMetadata, `{"turn_id":"ws-1"}`)

	captureCodexWebsocketSessionHeaders(auth, reqHeaders, resp)

	state, ok := helps.GetCodexSession("auth-ws|pc-ws-1")
	if !ok {
		t.Fatalf("expected cached websocket session entry")
	}
	if state.SessionID != "sess-ws-stable" {
		t.Fatalf("SessionID = %q, want sess-ws-stable", state.SessionID)
	}
	if state.TurnState != "ws-captured-token" {
		t.Fatalf("TurnState = %q, want ws-captured-token", state.TurnState)
	}
	if state.TurnMetadata != `{"turn_id":"ws-1"}` {
		t.Fatalf("TurnMetadata = %q, want %q", state.TurnMetadata, `{"turn_id":"ws-1"}`)
	}
}

func TestCaptureCodexWebsocketSessionHeaders_SkipsWithoutConversationID(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set(codexHeaderTurnState, "ignored")
	captureCodexWebsocketSessionHeaders(&cliproxyauth.Auth{ID: "auth-ws"}, http.Header{}, resp)

	if _, ok := helps.GetCodexSession("auth-ws|"); ok {
		t.Fatalf("no entry should be created without a Conversation_id")
	}
}

func TestApplyCodexHeaders_OriginatorFromConfigBeatsDefault(t *testing.T) {
	t.Setenv(misc.CodexOriginatorEnvVar, "")
	auth := &cliproxyauth.Auth{ID: "auth-orig"}
	cfg := &config.Config{}
	cfg.CodexHeaderDefaults.Originator = "codex_atlas"

	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	applyCodexHeaders(req, auth, "tok", true, cfg)
	if got := req.Header.Get("Originator"); got != "codex_atlas" {
		t.Fatalf("Originator = %q, want codex_atlas", got)
	}
}

func TestApplyCodexHeaders_OriginatorFromEnv(t *testing.T) {
	t.Setenv(misc.CodexOriginatorEnvVar, "codex_vscode")
	auth := &cliproxyauth.Auth{ID: "auth-orig-env"}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	applyCodexHeaders(req, auth, "tok", true, nil)
	if got := req.Header.Get("Originator"); got != "codex_vscode" {
		t.Fatalf("Originator = %q, want codex_vscode", got)
	}
}

func TestApplyCodexHeaders_OriginatorAPIKeySkipped(t *testing.T) {
	t.Setenv(misc.CodexOriginatorEnvVar, "codex_vscode")
	// API-key auths must not gain an Originator header unless the caller explicitly sent one,
	// preserving compatibility with OpenAI-compatible/custom base_url endpoints.
	auth := &cliproxyauth.Auth{ID: "auth-apikey", Attributes: map[string]string{"api_key": "sk-..."}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	applyCodexHeaders(req, auth, "tok", true, nil)
	if got := req.Header.Get("Originator"); got != "" {
		t.Fatalf("API-key path must not set Originator, got %q", got)
	}
}

func TestApplyCodexHeaders_ResidencyFromConfigAndClientPassthrough(t *testing.T) {
	t.Setenv(misc.CodexResidencyEnvVar, "")
	auth := &cliproxyauth.Auth{ID: "auth-residency"}

	// 1. config-provided residency applied when configured.
	cfg := &config.Config{}
	cfg.CodexHeaderDefaults.Residency = "eu-west"
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	applyCodexHeaders(req, auth, "tok", true, cfg)
	if got := req.Header.Get(misc.CodexResidencyHeader); got != "eu-west" {
		t.Fatalf("residency = %q, want eu-west", got)
	}

	// 2. client-supplied value beats config.
	req2, _ := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	req2.Header.Set(misc.CodexResidencyHeader, "us-central")
	// We still need the ginHeaders path to carry it; applyCodexHeaders pulls
	// from the request's existing Header for the X-GIN-* set inside the call.
	applyCodexHeaders(req2, auth, "tok", true, cfg)
	if got := req2.Header.Get(misc.CodexResidencyHeader); got == "" {
		t.Fatalf("client residency must be preserved")
	}
}

func TestApplyCodexHeaders_SubagentPassthrough(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "auth-sub"}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set(misc.CodexSubagentHeader, "planner")
	applyCodexHeaders(req, auth, "tok", true, nil)
	if got := req.Header.Get(misc.CodexSubagentHeader); got != "planner" {
		t.Fatalf("subagent passthrough = %q, want planner", got)
	}
}

func TestApplyCodexHeaders_ReplaysCachedTurnState(t *testing.T) {
	helps.ClearCodexSessions()
	t.Cleanup(helps.ClearCodexSessions)

	auth := &cliproxyauth.Auth{ID: "auth-replay", Metadata: map[string]any{"email": "u@example.com"}}
	helps.UpdateCodexSession("auth-replay|pc-5", func(s *helps.CodexSessionState) {
		s.SessionID = "sess-replay"
		s.TurnState = "sticky-token"
		s.TurnMetadata = `{"turn_id":"t-replay"}`
	})

	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	// Simulate what prepareCodexRequest does: set Session_id to the prompt
	// cache id so applyCodexHeaders can reconstruct the logical-session key.
	req.Header.Set("Session_id", "pc-5")

	applyCodexHeaders(req, auth, "tok", true, nil)

	if got := req.Header.Get(codexHeaderTurnState); got != "sticky-token" {
		t.Fatalf("turn-state not replayed: got %q", got)
	}
	if got := req.Header.Get(codexHeaderTurnMetadata); got != `{"turn_id":"t-replay"}` {
		t.Fatalf("turn-metadata not replayed: got %q", got)
	}
}
