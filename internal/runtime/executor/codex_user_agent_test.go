package executor

import (
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexDefaultUserAgentMatchesUpstreamShape(t *testing.T) {
	got := codexUserAgent

	pattern := regexp.MustCompile(`^codex_cli_rs/[^ ]+ \([^;]+; [^)]+\) \S+$`)
	if !pattern.MatchString(got) {
		t.Fatalf("codexUserAgent = %q does not match codex-rs user agent shape", got)
	}
	if got != misc.CodexCLIUserAgent {
		t.Fatalf("codexUserAgent (%q) must equal misc.CodexCLIUserAgent (%q)", got, misc.CodexCLIUserAgent)
	}
}

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
