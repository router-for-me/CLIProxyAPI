package misc

import (
	"strings"
	"testing"
)

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
