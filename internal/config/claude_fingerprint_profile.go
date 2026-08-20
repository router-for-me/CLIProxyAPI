package config

import (
	"fmt"
	"strings"
)

// Claude fingerprint profile values for ClaudeKey.FingerprintProfile and for the
// matching auth-file / auth-attribute field. This is the single source of truth:
// the runtime, the config sanitizer and the Management API all resolve a raw
// value through NormalizeClaudeFingerprintProfile so an operator cannot end up
// with a value that one layer accepts and another silently ignores.
const (
	// ClaudeFingerprintProfileDefault keeps the caller-owned request fingerprint.
	ClaudeFingerprintProfileDefault = ""
	// ClaudeFingerprintProfileClaudeCodeCLI opts into the Claude Code CLI Messages fingerprint.
	ClaudeFingerprintProfileClaudeCodeCLI = "claude-code-cli"
	// claudeFingerprintProfileOAuthCLIAlias is the legacy spelling of claude-code-cli.
	claudeFingerprintProfileOAuthCLIAlias = "oauth-cli"
)

// NormalizeClaudeFingerprintProfile maps a raw configured value to its canonical
// form. The second result reports whether the value is recognized; an
// unrecognized value normalizes to the default (caller-owned) profile.
func NormalizeClaudeFingerprintProfile(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ClaudeFingerprintProfileClaudeCodeCLI, claudeFingerprintProfileOAuthCLIAlias:
		return ClaudeFingerprintProfileClaudeCodeCLI, true
	case ClaudeFingerprintProfileDefault:
		return ClaudeFingerprintProfileDefault, true
	default:
		return ClaudeFingerprintProfileDefault, false
	}
}

// ValidateClaudeFingerprintProfile reports an error for values that would be
// silently ignored at request time. Write paths (Management API) use it to
// reject a typo instead of letting it reach the request path.
func ValidateClaudeFingerprintProfile(raw string) error {
	if _, ok := NormalizeClaudeFingerprintProfile(raw); !ok {
		return fmt.Errorf("unsupported fingerprint-profile %q (supported: %q or empty)", strings.TrimSpace(raw), ClaudeFingerprintProfileClaudeCodeCLI)
	}
	return nil
}
