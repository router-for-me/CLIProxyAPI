package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestClaudeExecutor_DirectMessagesOAuthDefaultsToCloak_Issue6120 verifies Option A for Issue #6120:
// Direct Anthropic Messages requests using Claude OAuth credentials without explicit cloak configuration
// must be cloaked by default to prevent Anthropic 429/400 rejections and credential cooldown.
func TestClaudeExecutor_DirectMessagesOAuthDefaultsToCloak_Issue6120(t *testing.T) {
	ctx := directClaudeMessagesContext()
	payload := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"caller system"}],"messages":[{"role":"user","content":"hello"}]}`)
	auth := directClaudeOAuthAuth()

	// 1. Verify applyCloaking cloaks by default on direct Messages for unconfirmed OAuth caller
	gotPayload, cloaked, err := applyCloaking(ctx, &config.Config{}, auth, payload, "sk-ant-oat-direct-messages-test", false, true)
	if err != nil {
		t.Fatalf("applyCloaking() error = %v", err)
	}
	if !cloaked {
		t.Fatal("applyCloaking() cloaked = false, want true (Issue #6120: unconfirmed OAuth direct Messages callers must be cloaked by default)")
	}
	if !gjson.GetBytes(gotPayload, "system.#(text==\"You are Claude Code, Anthropic's official CLI for Claude.\")").Exists() {
		t.Fatal("expected cloaked payload to contain Claude Code CLI identity system block")
	}

	// 2. Verify ClaudeExecutor.Execute applies CLI fingerprint and cloaked headers upstream
	var seenHeaders http.Header
	var seenBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"msg_test","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})

	execCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	incoming := http.Header{
		"User-Agent":     {"pi (linux; x64)"},
		"Anthropic-Beta": {"caller-beta-2099-01-01"},
	}
	authWithURL := directClaudeOAuthAuth()
	authWithURL.Attributes["base_url"] = "https://api.anthropic.com"

	_, errExec := NewClaudeExecutor(&config.Config{}).Execute(execCtx, authWithURL, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incoming,
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	if ua := seenHeaders.Get("User-Agent"); ua == incoming.Get("User-Agent") || !strings.Contains(ua, "claude-cli") {
		t.Fatalf("User-Agent = %q, want cloaked CLI User-Agent instead of raw third-party caller User-Agent", ua)
	}
	if xApp := helps.HeaderValueCaseInsensitive(seenHeaders, "X-App"); xApp != "cli" {
		t.Fatalf("X-App = %q, want %q", xApp, "cli")
	}
	betas := helps.HeaderValueCaseInsensitive(seenHeaders, "Anthropic-Beta")
	if !strings.Contains(betas, "claude-code-20250219") || !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want claude-code-20250219 and oauth-2025-04-20 for cloaked OAuth", betas)
	}
	if !gjson.GetBytes(seenBody, "system.#(text==\"You are Claude Code, Anthropic's official CLI for Claude.\")").Exists() {
		t.Fatal("expected upstream body to contain Claude Code CLI identity system block")
	}
}

// TestClaudeExecutor_DirectMessagesOAuthDefaultsToCloakStream_Issue6120 verifies Option A for streaming:
// Direct Anthropic Messages streaming requests using Claude OAuth credentials without explicit cloak configuration
// must also be cloaked by default upstream.
func TestClaudeExecutor_DirectMessagesOAuthDefaultsToCloakStream_Issue6120(t *testing.T) {
	var seenHeaders http.Header
	var seenBody []byte
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seenBody, _ = io.ReadAll(req.Body)
		seenHeaders = req.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			)),
			Request: req,
		}, nil
	})

	execCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	incoming := http.Header{
		"User-Agent":     {"pi (linux; x64)"},
		"Anthropic-Beta": {"caller-beta-2099-01-01"},
	}
	authWithURL := directClaudeOAuthAuth()
	authWithURL.Attributes["base_url"] = "https://api.anthropic.com"
	payload := []byte(`{"model":"claude-opus-5-5","system":[{"type":"text","text":"caller system"}],"messages":[{"role":"user","content":"hello"}]}`)

	streamResp, errStream := NewClaudeExecutor(&config.Config{}).ExecuteStream(execCtx, authWithURL, cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Headers:      incoming,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range streamResp.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	if ua := seenHeaders.Get("User-Agent"); ua == incoming.Get("User-Agent") || !strings.Contains(ua, "claude-cli") {
		t.Fatalf("streaming User-Agent = %q, want cloaked CLI User-Agent", ua)
	}
	if xApp := helps.HeaderValueCaseInsensitive(seenHeaders, "X-App"); xApp != "cli" {
		t.Fatalf("streaming X-App = %q, want %q", xApp, "cli")
	}
	betas := helps.HeaderValueCaseInsensitive(seenHeaders, "Anthropic-Beta")
	if !strings.Contains(betas, "claude-code-20250219") || !strings.Contains(betas, "oauth-2025-04-20") {
		t.Fatalf("streaming Anthropic-Beta = %q, want claude-code-20250219 and oauth-2025-04-20", betas)
	}
	if !gjson.GetBytes(seenBody, "system.#(text==\"You are Claude Code, Anthropic's official CLI for Claude.\")").Exists() {
		t.Fatal("expected streaming upstream body to contain Claude Code CLI identity system block")
	}
}
