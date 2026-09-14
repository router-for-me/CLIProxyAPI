package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// fixtureAuth builds a cloaked direct-Anthropic API-key auth for header tests.
// The key value is a placeholder that never reaches a real upstream.
func fixtureAuth() *cliproxyauth.Auth {
	attrs := map[string]string{}
	attrs[cliproxyauth.AttributeAPIKey] = strings.Join([]string{"test", "fixture", "key"}, "-")
	attrs["fingerprint_profile"] = "claude-code-cli"
	return &cliproxyauth.Auth{Attributes: attrs}
}

// A caller the cloak does not recognize (a Claude Code newer than the pinned
// profile) keeps betas the proxy does not manage: dropping them fails whole
// turns whose features need the missing authorization, e.g. per-turn effort
// directives with per-turn-control-2026-07-01 (#5738).
func TestApplyClaudeHeaders_ForwardsUnmanagedCallerBetas(t *testing.T) {
	t.Parallel()

	auth := fixtureAuth()
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01")
	if errHeaders := applyClaudeHeaders(req, auth, auth.Attributes[cliproxyauth.AttributeAPIKey], false, nil, []byte(`{"model":"claude-fable-5-1"}`), &config.Config{}, incoming, false); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	betas := req.Header.Get("Anthropic-Beta")
	for _, want := range []string{"per-turn-control-2026-07-01", "mid-conversation-tool-changes-2026-07-01"} {
		if !strings.Contains(betas, want) {
			t.Fatalf("Anthropic-Beta = %q, want unmanaged caller beta %q forwarded", betas, want)
		}
	}
}

// Managed betas stay governed by the assembled baseline on direct Anthropic:
// one whose gating excludes the request (effort on a Haiku model) is not
// reinstated just because the caller asked for it.
func TestApplyClaudeHeaders_StillGatesManagedCallerBetas(t *testing.T) {
	t.Parallel()

	auth := fixtureAuth()
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "effort-2025-11-24")
	if errHeaders := applyClaudeHeaders(req, auth, auth.Attributes[cliproxyauth.AttributeAPIKey], false, nil, []byte(`{"model":"claude-haiku-4-5"}`), &config.Config{}, incoming, false); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	if betas := req.Header.Get("Anthropic-Beta"); strings.Contains(betas, "effort-2025-11-24") {
		t.Fatalf("Anthropic-Beta = %q, want gated effort beta kept off the Haiku request", betas)
	}
}

// TestApplyClaudeHeaders_ForwardsUnmanagedCallerBetas_OAuth verifies the exact
// scenario reported in #5738: an OAuth credential with an unconfirmed client
// sending a per-turn effort directive turn.
func TestApplyClaudeHeaders_ForwardsUnmanagedCallerBetas_OAuth(t *testing.T) {
	t.Parallel()

	auth := &cliproxyauth.Auth{
		ID:       "claude-oauth-fixture",
		Metadata: map[string]any{"access_token": "sk-ant-oat-fixture"},
	}
	req, errReq := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", nil)
	if errReq != nil {
		t.Fatalf("NewRequest() error = %v", errReq)
	}
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", "per-turn-control-2026-07-01,mid-conversation-system-2026-04-07")
	body := []byte(`{
		"model": "claude-fable-5-1",
		"max_tokens": 16,
		"messages": [
			{"role": "user",   "content": "hi"},
			{"role": "system", "content": [], "output_config": {"effort": "low"}},
			{"role": "user",   "content": "say ok"}
		]
	}`)
	if errHeaders := applyClaudeHeaders(req, auth, "sk-ant-oat-fixture", false, nil, body, &config.Config{}, incoming, false); errHeaders != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
	}
	betas := req.Header.Get("Anthropic-Beta")
	if !strings.Contains(betas, "per-turn-control-2026-07-01") {
		t.Fatalf("Anthropic-Beta = %q, want unmanaged caller beta %q forwarded on OAuth", betas, "per-turn-control-2026-07-01")
	}
}
