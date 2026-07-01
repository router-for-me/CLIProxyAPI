package executor

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestIsOfficialCodexUserAgent(t *testing.T) {
	official := []string{
		"codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)",
		"codex_cli_rs/0.1.0",
		"codex-cli/1.0",
		"codex-exec/2.3",
		"OpenCode/2.0", // case-insensitive
	}
	foreign := []string{
		"",
		"OpenAI/Python 1.2.3",
		"python-requests/2.31.0",
		"Go-http-client/2.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
		"my-codex-client/1.0", // not a first-party prefix; only honored via config
	}
	for _, ua := range official {
		if !isOfficialCodexUserAgent(ua) {
			t.Errorf("expected official Codex UA: %q", ua)
		}
	}
	for _, ua := range foreign {
		if isOfficialCodexUserAgent(ua) {
			t.Errorf("expected non-official UA: %q", ua)
		}
	}
}

func codexOAuthAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
}

func TestApplyCodexWebsocketHeadersReplacesForeignClientUA(t *testing.T) {
	// Arrange: OAuth account + downstream client sends a non-Codex User-Agent, no config.
	ctx := contextWithGinHeaders(map[string]string{"User-Agent": "OpenAI/Python 1.2.3"})

	// Act
	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, codexOAuthAuth(), "", nil)

	// Assert: the foreign UA must NOT leak to the ChatGPT backend; canonical Codex UA is used.
	if got := headers.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %q, want canonical %q", got, codexUserAgent)
	}
}

func TestApplyCodexWebsocketHeadersKeepsOfficialClientUA(t *testing.T) {
	// Arrange: OAuth account + downstream client is a real Codex CLI.
	ctx := contextWithGinHeaders(map[string]string{"User-Agent": "codex_cli_rs/0.1.0"})

	// Act
	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, codexOAuthAuth(), "", nil)

	// Assert: a recognized Codex UA is forwarded verbatim.
	if got := headers.Get("User-Agent"); got != "codex_cli_rs/0.1.0" {
		t.Fatalf("User-Agent = %q, want %q", got, "codex_cli_rs/0.1.0")
	}
}
