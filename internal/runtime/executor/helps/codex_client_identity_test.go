package helps

import (
	"net/http"
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

func TestNativeCodexUserAgent(t *testing.T) {
	const nativeUserAgent = "codex-tui/0.155.1 (Mac OS 15.7.9; arm64) Apple_Terminal/455.1 (codex-tui; 0.155.1)"

	nativeHeaders := func() http.Header {
		headers := http.Header{}
		headers.Set("User-Agent", nativeUserAgent)
		headers.Set("Originator", "codex-tui")
		return headers
	}

	if got := NativeCodexUserAgent(&config.Config{}, nativeHeaders()); got != nativeUserAgent {
		t.Errorf("first-party identity = %q, want the downstream value", got)
	}
	if got := NativeCodexUserAgent(nil, nativeHeaders()); got != nativeUserAgent {
		t.Errorf("nil config = %q, want the downstream value", got)
	}
	if got := NativeCodexUserAgent(&config.Config{}, nil); got != "" {
		t.Errorf("nil headers = %q, want empty", got)
	}

	disabled := false
	cfgDisabled := &config.Config{Codex: config.CodexConfig{PreserveNativeClientIdentity: &disabled}}
	if got := NativeCodexUserAgent(cfgDisabled, nativeHeaders()); got != "" {
		t.Errorf("preservation disabled = %q, want empty so the configured default applies", got)
	}

	thirdParty := http.Header{}
	thirdParty.Set("User-Agent", "my-proxy/1.0.0")
	thirdParty.Set("Originator", "my-proxy")
	if got := NativeCodexUserAgent(&config.Config{}, thirdParty); got != "" {
		t.Errorf("third-party identity = %q, want empty so cloaking applies", got)
	}
}
