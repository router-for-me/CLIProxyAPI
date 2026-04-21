package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

func TestCodexResolvedUserAgentPrefersConfiguredUserAgent(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"header:User-Agent": "auth-file-ua",
			"header:Originator": "codex_vscode",
		},
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent: "config-ua",
		},
	}

	got := codexResolvedUserAgent(nil, nil, auth, cfg)
	if got != "config-ua" {
		t.Fatalf("codexResolvedUserAgent() = %q, want %q", got, "config-ua")
	}
}
