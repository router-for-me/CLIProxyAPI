package executor

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestIsOfficialCodexUserAgent(t *testing.T) {
	official := []string{
		"codex-tui/0.135.0 (Mac OS 26.2.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)",
		"codex_cli_rs/0.1.0",
		"codex_exec/2.3", // real exec UA prefix is underscore (first-hand captured: codex_exec/1.3.0)
		"codex_vscode/0.55.0 (Mac OS 15.1.0; arm64) WezTerm/20240203", // bound to codex_vscode originator
	}
	foreign := []string{
		"",
		"OpenAI/Python 1.2.3",
		"python-requests/2.31.0",
		"Go-http-client/2.0",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
		"my-codex-client/1.0", // not a first-party prefix; only honored via config
		"codex-cli/1.0",       // not a confirmed first-party token → normalized
		"codex-exec/2.3",      // hyphen form is NOT real; real exec UA = codex_exec/ (underscore) → normalized
		"opencode/2.0",        // third-party agent → normalized to official UA
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

func TestApplyCodexHeadersDoesNotSetConnectionHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, codexOAuthAuth(), "oauth-token", false, nil)

	if got := req.Header.Get("Connection"); got != "" {
		t.Fatalf("Connection = %q, want empty; HTTP/2 transport should manage connection reuse", got)
	}
}

func TestIsOfficialCodexOriginator(t *testing.T) {
	for _, o := range []string{"codex_cli_rs", "codex-tui", "Codex_Exec", "codex_vscode"} {
		if !isOfficialCodexOriginator(o) {
			t.Errorf("want official Codex originator: %q", o)
		}
	}
	// "Codex Desktop" and the injection-style values must NOT match: a loose "codex"
	// prefix would forward them verbatim while the UA is normalized to codex_cli_rs,
	// a cross-layer mismatch the observatory caught live on real accounts.
	for _, o := range []string{"", "python", "vscode", "my-app", "Codex Desktop", "codexZZZ", "codex/evil", "codex"} {
		if isOfficialCodexOriginator(o) {
			t.Errorf("want foreign originator: %q", o)
		}
	}
}

func TestSetCodexOriginator(t *testing.T) {
	cases := []struct {
		name      string
		client    string
		isAPIKey  bool
		wantValue string
	}{
		{"oauth_foreign_normalized", "python-sdk", false, codexOriginator},
		{"oauth_official_forwarded", "codex_cli_rs", false, "codex_cli_rs"},
		{"oauth_empty_canonical", "", false, codexOriginator},
		{"apikey_forwarded_verbatim", "python-sdk", true, "python-sdk"},
		{"apikey_empty_unset", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			setCodexOriginator(h, tc.client, tc.isAPIKey)
			if got := h.Get("Originator"); got != tc.wantValue {
				t.Fatalf("Originator = %q, want %q", got, tc.wantValue)
			}
		})
	}
}
