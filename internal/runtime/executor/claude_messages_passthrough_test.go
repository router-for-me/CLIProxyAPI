package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func directClaudeMessagesContext() context.Context {
	return context.Background()
}

func directClaudeOAuthAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "sk-ant-oat-direct-messages-test",
		},
		Metadata: claudeOAuthTestMetadata(),
	}
}

func TestApplyClaudeCloak_DirectMessagesPreservesCallerBody(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"caller system"}],"thinking":{"type":"adaptive","display":"summarized"},"tools":[{"name":"caller_tool"}],"messages":[{"role":"user","content":"hello"}]}`)

	authNever := directClaudeOAuthAuth()
	authNever.Attributes["cloak_mode"] = "never"
	got, cloaked, err := applyCloaking(directClaudeMessagesContext(), &config.Config{}, authNever, payload, "sk-ant-oat-direct-messages-test", false, true)
	if err != nil {
		t.Fatalf("applyCloaking() error = %v", err)
	}
	if cloaked {
		t.Fatal("applyCloaking() cloaked = true, want direct Messages passthrough when cloak_mode is never")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("direct Messages body changed: got %s, want %s", got, payload)
	}
}

func TestApplyClaudeHeaders_DirectMessagesPreservesCallerFingerprint(t *testing.T) {
	ctx := directClaudeMessagesContext()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	incoming := http.Header{
		"User-Agent":     {"pi (darwin; arm64)"},
		"Anthropic-Beta": {"caller-beta-2099-01-01"},
	}
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"}}`)

	if err := applyClaudeHeaders(req, directClaudeAPIKeyAuth(), "sk-ant-api03-direct-messages-test", false, nil, body, &config.Config{}, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("User-Agent"); got != incoming.Get("User-Agent") {
		t.Fatalf("User-Agent = %q, want caller value %q", got, incoming.Get("User-Agent"))
	}
	if got := req.Header.Get("Anthropic-Beta"); got != incoming.Get("Anthropic-Beta") {
		t.Fatalf("Anthropic-Beta = %q, want caller value %q", got, incoming.Get("Anthropic-Beta"))
	}
	if got := req.Header.Get("X-App"); got != "" {
		t.Fatalf("X-App = %q, want absent in direct Messages caller-owned API key mode", got)
	}
}

func TestClaudeExecutor_DirectMessagesOfficialUpstreamPreservesCallerShape(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_direct","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	incoming := http.Header{
		"User-Agent":     {"pi (darwin; arm64)"},
		"Anthropic-Beta": {"caller-beta-2099-01-01"},
	}
	auth := directClaudeAPIKeyAuth()
	auth.Attributes["base_url"] = "https://api.anthropic.com"
	payload := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"caller system","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"adaptive","display":"summarized"},"tools":[{"name":"caller_tool","description":"caller tool","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)

	_, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incoming,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := seenHeaders.Get("User-Agent"); got != incoming.Get("User-Agent") {
		t.Fatalf("User-Agent = %q, want %q; headers=%v", got, incoming.Get("User-Agent"), seenHeaders)
	}
	if got := helps.HeaderValueCaseInsensitive(seenHeaders, "Anthropic-Beta"); got != incoming.Get("Anthropic-Beta") {
		t.Fatalf("Anthropic-Beta = %q, want %q; headers=%v", got, incoming.Get("Anthropic-Beta"), seenHeaders)
	}
	if got := gjson.GetBytes(seenBody, "system.#").Int(); got != 1 {
		t.Fatalf("system block count = %d, want the caller block only", got)
	}
	if gjson.GetBytes(seenBody, "system.#(text==\"You are Claude Code, Anthropic's official CLI for Claude.\")").Exists() {
		t.Fatal("direct Messages request gained the CLI identity system block")
	}
	if got := gjson.GetBytes(seenBody, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized", got)
	}
	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "caller_tool" {
		t.Fatalf("tool name = %q, want caller_tool", got)
	}
	if gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
		t.Fatal("direct Messages request gained a synthetic cache breakpoint")
	}
}

func directClaudeAPIKeyAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key": "sk-ant-api03-direct-messages-test",
		},
	}
}

func TestApplyClaudeHeaders_DirectMessagesAPIKeyPreservesExactCallerBetas(t *testing.T) {
	ctx := directClaudeMessagesContext()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	incoming := http.Header{
		"User-Agent":     {"pi (darwin; arm64)"},
		"Anthropic-Beta": {"caller-beta-2099-01-01"},
	}
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"}}`)

	if err := applyClaudeHeaders(req, directClaudeAPIKeyAuth(), "sk-ant-api03-direct-messages-test", false, nil, body, &config.Config{}, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != "caller-beta-2099-01-01" {
		t.Fatalf("Anthropic-Beta = %q, want caller value %q (no oauth beta for api key)", got, "caller-beta-2099-01-01")
	}
}

func TestApplyClaudeHeaders_DirectMessagesOAuthInjectsOAuthBetaWhenEmpty(t *testing.T) {
	ctx := directClaudeMessagesContext()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	incoming := http.Header{
		"User-Agent": {"pi (darwin; arm64)"},
	}
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"}}`)

	auth := directClaudeOAuthAuth()
	if err := applyClaudeHeaders(req, auth, "sk-ant-oat-direct-messages-test", false, nil, body, &config.Config{}, incoming, false); err != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", err)
	}
	betas := req.Header.Get("Anthropic-Beta")
	if !strings.Contains(betas, "oauth-2025-04-20") || !strings.Contains(betas, "claude-code-20250219") {
		t.Fatalf("Anthropic-Beta = %q, want oauth-2025-04-20 and claude-code-20250219 for OAuth", betas)
	}
}

func TestClaudeExecutor_PreserveCallerFingerprintAlignsSessionIDWithBody(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_direct","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	incoming := http.Header{
		"User-Agent":                  {"pi (darwin; arm64)"},
		"X-Claude-Code-Session-Id":    {"forged-session-a"},
		"X-Claude-Code-Request-Class": {"forged-class"},
	}
	auth := directClaudeOAuthAuth()
	auth.Attributes["base_url"] = "https://api.anthropic.com"
	payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)

	_, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incoming,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	headerSession := seenHeaders.Get("X-Claude-Code-Session-Id")
	if headerSession == "forged-session-a" {
		t.Fatalf("X-Claude-Code-Session-Id forwarded forged unconfirmed session %q", headerSession)
	}
	if headerSession == "" {
		t.Fatal("X-Claude-Code-Session-Id is empty, want authoritative session ID")
	}

	// Verify header session matches session_id embedded in metadata.user_id:
	rawUserID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	bodySession := gjson.Get(rawUserID, "session_id").String()
	if bodySession == "" {
		t.Fatalf("metadata.user_id has no session_id: %s", rawUserID)
	}
	if headerSession != bodySession {
		t.Fatalf("header session %q != body session %q", headerSession, bodySession)
	}

	// Verify unconfirmed caller's X-Claude-Code-Request-Class was not leaked:
	if got := seenHeaders.Get("X-Claude-Code-Request-Class"); got != "" {
		t.Fatalf("X-Claude-Code-Request-Class = %q, want stripped for unconfirmed caller", got)
	}
}

func TestApplyClaudeCloakThinkingDisplay(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		model string
		want  string
	}{
		{name: "opus adaptive", model: "claude-opus-5-5", body: `{"model":"claude-opus-5-5","thinking":{"type":"adaptive"}}`, want: "updates"},
		{name: "fable enabled", model: "claude-fable-5-1", body: `{"model":"claude-fable-5-1","thinking":{"type":"enabled"}}`, want: "updates"},
		{name: "sonnet adaptive", model: "claude-sonnet-5", body: `{"model":"claude-sonnet-5","thinking":{"type":"adaptive"}}`, want: "updates"},
		{name: "explicit summarized", model: "claude-opus-5-5", body: `{"model":"claude-opus-5-5","thinking":{"type":"adaptive","display":"summarized"}}`, want: "summarized"},
		{name: "explicit omitted", model: "claude-sonnet-5", body: `{"model":"claude-sonnet-5","thinking":{"type":"adaptive","display":"omitted"}}`, want: "omitted"},
		{name: "disabled", model: "claude-opus-5-5", body: `{"model":"claude-opus-5-5","thinking":{"type":"disabled"}}`, want: ""},
		{name: "legacy model", model: "claude-opus-4-6", body: `{"model":"claude-opus-4-6","thinking":{"type":"adaptive"}}`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := applyClaudeCloakThinkingDisplay([]byte(test.body), false)
			if display := gjson.GetBytes(got, "thinking.display").String(); display != test.want {
				t.Fatalf("model=%s thinking.display = %q, want %q; body=%s", test.model, display, test.want, got)
			}
		})
	}
}
