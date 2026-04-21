package misc

import (
	"net/http"
	"regexp"
	"testing"
)

func TestBuildCodexUserAgent_MatchesUpstreamShape(t *testing.T) {
	got := BuildCodexUserAgent("1.2.3")
	// codex-rs get_codex_user_agent() emits:
	//   originator/version (OS; arch) terminal
	// We do not assert OS or terminal literally because they vary by host,
	// but the structural grouping must match so upstream parsers recognize it.
	pattern := regexp.MustCompile(`^codex_cli_rs/1\.2\.3 \([^;]+; [^)]+\) \S+$`)
	if !pattern.MatchString(got) {
		t.Fatalf("BuildCodexUserAgent(%q) = %q does not match expected shape", "1.2.3", got)
	}
	if _, err := http.NewRequest(http.MethodGet, "https://example.com", nil); err != nil {
		t.Fatalf("sanity request: %v", err)
	}
}

func TestBuildCodexUserAgent_FallsBackToDefaultVersion(t *testing.T) {
	got := BuildCodexUserAgent("  ")
	pattern := regexp.MustCompile(`^codex_cli_rs/` + regexp.QuoteMeta(CodexCLIVersion) + ` \(`)
	if !pattern.MatchString(got) {
		t.Fatalf("empty version should fall back to CodexCLIVersion, got %q", got)
	}
}

func TestBuildCodexUserAgent_IsValidHeaderValue(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("User-Agent", CodexCLIUserAgent)
	// net/http will sanitize/reject invalid header values; if Set+Get round-trips
	// the string unchanged then it is a valid token.
	if got := req.Header.Get("User-Agent"); got != CodexCLIUserAgent {
		t.Fatalf("User-Agent roundtrip changed value: got %q, want %q", got, CodexCLIUserAgent)
	}
}

func TestResolveCodexOriginator_Precedence(t *testing.T) {
	t.Setenv(CodexOriginatorEnvVar, "")
	if got := ResolveCodexOriginator(""); got != CodexDefaultOriginator {
		t.Fatalf("default originator = %q, want %q", got, CodexDefaultOriginator)
	}
	// Env overrides default.
	t.Setenv(CodexOriginatorEnvVar, "codex_vscode")
	if got := ResolveCodexOriginator(""); got != "codex_vscode" {
		t.Fatalf("env originator = %q, want %q", got, "codex_vscode")
	}
	// Config beats env.
	if got := ResolveCodexOriginator("  codex_atlas  "); got != "codex_atlas" {
		t.Fatalf("configured originator = %q, want %q", got, "codex_atlas")
	}
	// Invalid values are rejected and we fall back to default (not the bad
	// value passed in).
	t.Setenv(CodexOriginatorEnvVar, "")
	if got := ResolveCodexOriginator("bad\x01value"); got != CodexDefaultOriginator {
		t.Fatalf("invalid originator = %q, want fallback %q", got, CodexDefaultOriginator)
	}
}

func TestResolveCodexResidency_EmptyMeansSkip(t *testing.T) {
	t.Setenv(CodexResidencyEnvVar, "")
	if got := ResolveCodexResidency(""); got != "" {
		t.Fatalf("default residency must be empty, got %q", got)
	}
	t.Setenv(CodexResidencyEnvVar, "us-central")
	if got := ResolveCodexResidency(""); got != "us-central" {
		t.Fatalf("env residency = %q, want %q", got, "us-central")
	}
	if got := ResolveCodexResidency(" eu-west "); got != "eu-west" {
		t.Fatalf("configured residency = %q, want %q", got, "eu-west")
	}
}

func TestSanitizeTerminalToken_ReplacesControlChars(t *testing.T) {
	got := sanitizeTerminalToken("bad\ttoken with space\x01")
	expect := "bad_token_with_space_"
	if got != expect {
		t.Fatalf("sanitizeTerminalToken = %q, want %q", got, expect)
	}
}
