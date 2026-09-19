package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestIsFirstPartyCodexIdentity(t *testing.T) {
	cases := []struct {
		name       string
		userAgent  string
		originator string
		want       bool
	}{
		{"real tui", "codex-tui/0.154.0 (Mac OS 15.7.9; arm64) Apple_Terminal (codex-tui; 0.154.0)", "codex-tui", true},
		{"codex prefix", "Codex Desktop/1.2.3 (Mac OS 15.7.9)", "Codex Desktop", true},
		{"vscode", "codex_vscode/1.0.0", "codex_vscode", true},
		{"exec", "codex_exec/0.1", "codex_exec", true},
		{"cli rs", "codex_cli_rs/1.2.3-beta.1 (x)", "codex_cli_rs", true},
		{"mismatched pair", "codex-tui/0.154.0 (Mac OS 15.7.9)", "Codex Desktop", false},
		{"third party", "my-proxy/1.0.0", "my-proxy", false},
		{"no version", "codex-tui/ (x)", "codex-tui", false},
		{"non numeric version", "codex-tui/dev", "codex-tui", false},
		{"empty user agent", "", "codex-tui", false},
		{"empty originator", "codex-tui/0.154.0", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFirstPartyCodexIdentity(tc.userAgent, tc.originator); got != tc.want {
				t.Errorf("IsFirstPartyCodexIdentity(%q, %q) = %t, want %t", tc.userAgent, tc.originator, got, tc.want)
			}
		})
	}
}

func TestPreserveNativeCodexIdentity(t *testing.T) {
	if !PreserveNativeCodexIdentity(nil) {
		t.Error("nil config should preserve")
	}
	if !PreserveNativeCodexIdentity(&config.Config{}) {
		t.Error("unset field should preserve")
	}
	disabled := false
	if PreserveNativeCodexIdentity(&config.Config{Codex: config.CodexConfig{PreserveNativeClientIdentity: &disabled}}) {
		t.Error("explicit false should not preserve")
	}
}
