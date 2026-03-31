package executor

import "testing"

func TestCodexDefaultUserAgentMatchesLinuxRelease(t *testing.T) {
	const want = "codex_cli_rs/0.118.0-alpha.4 (Linux; x86_64) xterm-256color"

	if got := codexUserAgent; got != want {
		t.Fatalf("codexUserAgent = %q, want %q", got, want)
	}
}
