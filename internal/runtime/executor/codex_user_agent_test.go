package executor

import (
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// TestCodexDefaultUserAgentMatchesUpstreamShape asserts that the dynamically
// built Codex CLI User-Agent follows the codex-rs get_codex_user_agent() shape:
//
//	codex_cli_rs/<version> (<OS>; <arch>) <terminal>
//
// We intentionally avoid pinning the exact bytes because the terminal token
// varies with the host's $TERM / $TERM_PROGRAM environment and the OS/arch
// tokens adapt to the runtime. The prior hard-coded string tied CI to Linux
// only, which would have broken on macOS/Windows developer workstations.
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
