package misc

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestBuildCodexUserAgent_MatchesUpstreamShape(t *testing.T) {
	got := BuildCodexUserAgent("1.2.3")
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
	if got := req.Header.Get("User-Agent"); got != CodexCLIUserAgent {
		t.Fatalf("User-Agent roundtrip changed value: got %q, want %q", got, CodexCLIUserAgent)
	}
}

func TestResolveCodexOriginator_Precedence(t *testing.T) {
	t.Setenv(CodexOriginatorEnvVar, "")
	if got := ResolveCodexOriginator(""); got != CodexDefaultOriginator {
		t.Fatalf("default originator = %q, want %q", got, CodexDefaultOriginator)
	}
	t.Setenv(CodexOriginatorEnvVar, "codex_vscode")
	if got := ResolveCodexOriginator(""); got != "codex_vscode" {
		t.Fatalf("env originator = %q, want %q", got, "codex_vscode")
	}
	if got := ResolveCodexOriginator("  codex_atlas  "); got != "codex_atlas" {
		t.Fatalf("configured originator = %q, want %q", got, "codex_atlas")
	}
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

func TestCodexTerminalFromEnvUsesTermProgramVersion(t *testing.T) {
	got := codexTerminalFromEnv(func(key string) string {
		switch key {
		case "TERM_PROGRAM":
			return "VTE"
		case "TERM_PROGRAM_VERSION":
			return "7600"
		case "TERM":
			return "xterm-256color"
		default:
			return ""
		}
	})

	if got != "VTE/7600" {
		t.Fatalf("codexTerminalFromEnv() = %q, want %q", got, "VTE/7600")
	}
}

func TestCodexTerminalFromEnvFallsBackToVTEVersion(t *testing.T) {
	got := codexTerminalFromEnv(func(key string) string {
		switch key {
		case "VTE_VERSION":
			return "7600"
		case "TERM":
			return "xterm-256color"
		default:
			return ""
		}
	})

	if got != "VTE/7600" {
		t.Fatalf("codexTerminalFromEnv() = %q, want %q", got, "VTE/7600")
	}
}

func TestCodexTerminalFromEnvSanitizesInvalidChars(t *testing.T) {
	got := codexTerminalFromEnv(func(key string) string {
		switch key {
		case "TERM_PROGRAM":
			return "bad term"
		case "TERM_PROGRAM_VERSION":
			return "1:2"
		default:
			return ""
		}
	})

	if got != "bad_term/1_2" {
		t.Fatalf("codexTerminalFromEnv() = %q, want %q", got, "bad_term/1_2")
	}
}

func TestCodexLinuxOSDescriptorPrefersNameAndVersionID(t *testing.T) {
	got := codexLinuxOSDescriptor(func(string) ([]byte, error) {
		return []byte("NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.2 LTS\"\n"), nil
	})

	if got != "Ubuntu 24.04" {
		t.Fatalf("codexLinuxOSDescriptor() = %q, want %q", got, "Ubuntu 24.04")
	}
}

func TestCodexLinuxOSDescriptorFallsBackToPrettyName(t *testing.T) {
	got := codexLinuxOSDescriptor(func(string) ([]byte, error) {
		return []byte("PRETTY_NAME=\"Fedora Linux 41\"\n"), nil
	})

	if got != "Fedora Linux 41" {
		t.Fatalf("codexLinuxOSDescriptor() = %q, want %q", got, "Fedora Linux 41")
	}
}

func TestCodexCLIUserAgentWithOriginatorTrimsWhitespace(t *testing.T) {
	got := CodexCLIUserAgentWithOriginator("  codex_vscode  ")

	if !strings.HasPrefix(got, "codex_vscode/") {
		t.Fatalf("CodexCLIUserAgentWithOriginator() = %q, want codex_vscode/ prefix", got)
	}
}

func TestCodexCLIUserAgentWithOriginatorFallsBackToDefaultOriginator(t *testing.T) {
	got := CodexCLIUserAgentWithOriginator(" \t ")

	if got != CodexCLIUserAgent {
		t.Fatalf("CodexCLIUserAgentWithOriginator() = %q, want %q", got, CodexCLIUserAgent)
	}
}

func TestCodexCLIUserAgentWithOriginatorUsesNormalizedCacheKey(t *testing.T) {
	left := CodexCLIUserAgentWithOriginator("codex_vscode")
	right := CodexCLIUserAgentWithOriginator(" codex_vscode ")

	if left != right {
		t.Fatalf("normalized originators should produce identical user agents: %q != %q", left, right)
	}
}
