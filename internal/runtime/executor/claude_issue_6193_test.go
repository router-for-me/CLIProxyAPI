package executor

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestIssue6193_Sonnet55ToolChangeBeta verifies that Sonnet 5.5 receives the
// mid-conversation-tool-changes-2026-07-01 beta, while Sonnet 5 remains excluded,
// and that on the Claude OAuth / CLI-fingerprint path the beta reaches Anthropic.
func TestIssue6193_Sonnet55ToolChangeBeta(t *testing.T) {
	// 1. In claudeCodeCLIBetas, Sonnet 5.5 (and dated/bracket variants) must include
	// mid-conversation-tool-changes-2026-07-01, while Sonnet 5 must remain excluded.
	sonnet55Variants := []string{
		"claude-sonnet-5-5",
		"claude-sonnet-5-5-20261001",
		"claude-sonnet-5-5[1m]",
	}
	for _, model := range sonnet55Variants {
		betas := claudeCodeCLIBetas([]byte(`{"model":"`+model+`"}`), nil, false)
		if !strings.Contains(betas, "mid-conversation-tool-changes-2026-07-01") {
			t.Fatalf("claudeCodeCLIBetas for %s missing mid-conversation-tool-changes-2026-07-01, got: %s", model, betas)
		}
		if isClaudeSonnet5Model(model) {
			t.Fatalf("isClaudeSonnet5Model(%q) = true, want false", model)
		}
		if !isClaudeSonnet55Model(model) {
			t.Fatalf("isClaudeSonnet55Model(%q) = false, want true", model)
		}
		if !claudeModelUsesProgressDisplay(model) {
			t.Fatalf("claudeModelUsesProgressDisplay(%q) = false, want true", model)
		}
	}

	sonnet5Variants := []string{
		"claude-sonnet-5",
		"claude-sonnet-5-20260401",
		"claude-sonnet-5[1m]",
	}
	for _, model := range sonnet5Variants {
		betas := claudeCodeCLIBetas([]byte(`{"model":"`+model+`"}`), nil, false)
		if strings.Contains(betas, "mid-conversation-tool-changes-2026-07-01") {
			t.Fatalf("claudeCodeCLIBetas for %s unexpectedly includes mid-conversation-tool-changes-2026-07-01, got: %s", model, betas)
		}
		if !isClaudeSonnet5Model(model) {
			t.Fatalf("isClaudeSonnet5Model(%q) = false, want true", model)
		}
		if isClaudeSonnet55Model(model) {
			t.Fatalf("isClaudeSonnet55Model(%q) = true, want false", model)
		}
		if !claudeModelUsesProgressDisplay(model) {
			t.Fatalf("claudeModelUsesProgressDisplay(%q) = false, want true", model)
		}
	}

	// 2. Outgoing Anthropic-Beta on Claude OAuth / CLI fingerprint path for claude-sonnet-5-5
	// must contain mid-conversation-tool-changes-2026-07-01, both when caller sends an explicit beta
	// and when caller sends no client beta.
	body := []byte(`{
		"model": "claude-sonnet-5-5",
		"max_tokens": 64,
		"tools": [{"name":"lookup_notes","input_schema":{"type":"object"}}],
		"messages": [
			{"role":"user","content":"hello"},
			{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"lookup_notes"}}]}
		]
	}`)

	auth := &cliproxyauth.Auth{
		ID:       "test-oauth",
		Metadata: map[string]any{"access_token": "sk-ant-oat-issue-6193"},
	}

	for _, tc := range []struct {
		name        string
		clientBetas string
	}{
		{name: "with explicit legacy beta", clientBetas: "mid-conversation-tool-changes-2026-07-01"},
		{name: "without client beta", clientBetas: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			incomingHeaders := http.Header{}
			if tc.clientBetas != "" {
				incomingHeaders.Set("Anthropic-Beta", tc.clientBetas)
			}

			req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("http.NewRequest failed: %v", err)
			}

			if errApply := applyClaudeHeaders(req, auth, "sk-ant-oat-issue-6193", false, nil, body, &config.Config{}, incomingHeaders, false); errApply != nil {
				t.Fatalf("applyClaudeHeaders failed: %v", errApply)
			}

			upstreamBetas := req.Header.Get("Anthropic-Beta")
			if !strings.Contains(upstreamBetas, "mid-conversation-tool-changes-2026-07-01") {
				t.Fatalf("applyClaudeHeaders for claude-sonnet-5-5 missing mid-conversation-tool-changes-2026-07-01, got Anthropic-Beta: %s", upstreamBetas)
			}
		})
	}
}
