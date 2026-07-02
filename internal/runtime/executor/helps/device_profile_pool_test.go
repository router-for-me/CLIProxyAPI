package helps

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const testCodexUA = "codex-tui/0.135.0 (Mac OS 26.5.0; arm64) iTerm.app/3.6.10 (codex-tui; 0.135.0)"

func authWithID(id string) *cliproxyauth.Auth { return &cliproxyauth.Auth{ID: id} }

func TestPerAccountClaudeProfile_DeterministicAndStable(t *testing.T) {
	scope := AccountFingerprintKey(authWithID("acct-stable"), "")
	first := perAccountClaudeProfile(scope, nil)
	for i := 0; i < 5; i++ {
		got := perAccountClaudeProfile(scope, nil)
		if got != first {
			t.Fatalf("profile not stable across calls: %+v vs %+v", got, first)
		}
	}
	if first.UserAgent == "" || first.PackageVersion == "" || first.RuntimeVersion == "" || first.OS == "" || first.Arch == "" {
		t.Fatalf("incomplete profile: %+v", first)
	}
}

func TestPerAccountClaudeProfile_ConfigOverrideWins(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeHeaderDefaults.UserAgent = "claude-cli/9.9.9 (external, cli)"
	p := perAccountClaudeProfile(AccountFingerprintKey(authWithID("x"), ""), cfg)
	if p.UserAgent != "claude-cli/9.9.9 (external, cli)" {
		t.Fatalf("config UA override ignored: %q", p.UserAgent)
	}
}

func TestAugmentClaudeDeviceHeaders_PreservesClientFillsRest(t *testing.T) {
	client := http.Header{}
	client.Set("User-Agent", "claude-cli/2.1.55 (external, cli)")
	client.Set("X-Stainless-Os", "Linux")

	out := AugmentClaudeDeviceHeaders(client, authWithID("acct-1"), "", nil)

	// Client-supplied values preserved.
	if out.Get("User-Agent") != "claude-cli/2.1.55 (external, cli)" {
		t.Fatalf("client UA not preserved: %q", out.Get("User-Agent"))
	}
	if out.Get("X-Stainless-Os") != "Linux" {
		t.Fatalf("client OS not preserved: %q", out.Get("X-Stainless-Os"))
	}
	// Absent device headers filled per-account.
	for _, h := range []string{"X-Stainless-Package-Version", "X-Stainless-Runtime-Version", "X-Stainless-Arch"} {
		if out.Get(h) == "" {
			t.Fatalf("expected %s to be filled", h)
		}
	}
	// The original client header map must not be mutated.
	if client.Get("X-Stainless-Arch") != "" {
		t.Fatalf("original client headers were mutated")
	}
}

func TestAugmentClaudeDeviceHeaders_DisabledReturnsClientUnchanged(t *testing.T) {
	cfg := &config.Config{DisableFingerprintRandomization: true}
	client := http.Header{}
	client.Set("User-Agent", "x")
	out := AugmentClaudeDeviceHeaders(client, authWithID("acct-1"), "", cfg)
	if out.Get("X-Stainless-Arch") != "" {
		t.Fatalf("randomization disabled but headers were filled")
	}
}

func TestFingerprint_PopulationIsDiverse(t *testing.T) {
	// Across many accounts, both Claude UA and Codex UA must span more than one
	// value — i.e. NOT a monoculture (the whole point vs stock CLIProxyAPI).
	claudeUAs := map[string]struct{}{}
	codexUAs := map[string]struct{}{}
	for i := 0; i < 40; i++ {
		scope := AccountFingerprintKey(authWithID(fmt.Sprintf("acct-%d", i)), "")
		claudeUAs[perAccountClaudeProfile(scope, nil).UserAgent] = struct{}{}
		ua := PerAccountCodexUserAgent(scope, testCodexUA, nil)
		codexUAs[ua] = struct{}{}
		if !strings.HasPrefix(ua, "codex_cli_rs/") {
			t.Fatalf("derived Codex UA not codex_cli_rs-shaped: %q", ua)
		}
	}
	if len(claudeUAs) < 2 {
		t.Fatalf("Claude UA population not diverse: %d distinct", len(claudeUAs))
	}
	if len(codexUAs) < 2 {
		t.Fatalf("Codex UA population not diverse: %d distinct", len(codexUAs))
	}
}

func TestPerAccountCodexUserAgent_StableAndFallback(t *testing.T) {
	scope := AccountFingerprintKey(authWithID("acct-codex"), "")
	a := PerAccountCodexUserAgent(scope, testCodexUA, nil)
	b := PerAccountCodexUserAgent(scope, testCodexUA, nil)
	if a != b {
		t.Fatalf("codex UA not stable: %q vs %q", a, b)
	}
	// Empty scope -> fallback.
	if got := PerAccountCodexUserAgent("", "fallback-ua", nil); got != "fallback-ua" {
		t.Fatalf("empty scope should return fallback, got %q", got)
	}
	// Disabled -> fallback.
	if got := PerAccountCodexUserAgent(scope, "fallback-ua", &config.Config{DisableFingerprintRandomization: true}); got != "fallback-ua" {
		t.Fatalf("disabled should return fallback, got %q", got)
	}
}
