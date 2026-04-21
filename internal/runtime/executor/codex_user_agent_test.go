package executor

import (
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexDefaultUserAgentUsesDefaultOriginatorIdentity(t *testing.T) {
	if !strings.HasPrefix(codexUserAgent, codexOriginator+"/") {
		t.Fatalf("codexUserAgent = %q, want %q prefix", codexUserAgent, codexOriginator+"/")
	}
	if !strings.Contains(codexUserAgent, "(") || !strings.Contains(codexUserAgent, ")") {
		t.Fatalf("codexUserAgent = %q, want platform segment", codexUserAgent)
	}
}

func TestCodexResolvedUserAgentFollowsAuthOriginatorFallback(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"header:Originator": "codex_vscode",
		},
	}

	got := codexResolvedUserAgent(nil, nil, auth, nil)
	if !strings.HasPrefix(got, "codex_vscode/") {
		t.Fatalf("codexResolvedUserAgent() = %q, want codex_vscode/ prefix", got)
	}
}
