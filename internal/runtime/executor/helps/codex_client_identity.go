package helps

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// codexFirstPartyOriginators lists the originator tokens the official Codex clients use.
var codexFirstPartyOriginators = map[string]struct{}{
	"codex_cli_rs": {},
	"codex-tui":    {},
	"codex_vscode": {},
	"codex_exec":   {},
}

// codexClientVersionPattern matches a semver-ish client version token such as 0.154.0 or 1.2.3-beta.1.
var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)*(?:[-+][0-9A-Za-z.-]+)?$`)

// IsFirstPartyCodexIdentity reports whether the pair of headers forms a coherent first-party Codex
// identity: the originator must be a known first-party token (or carry the "Codex " prefix) and the
// User-Agent must start with that same originator followed by "/" and a version token.
// The originator set follows is_first_party_originator in the Codex CLI, plus codex_exec, which the
// CLI emits in exec mode but omits from that particular whitelist.
func IsFirstPartyCodexIdentity(userAgent, originator string) bool {
	ua := strings.TrimSpace(userAgent)
	origin := strings.TrimSpace(originator)
	if ua == "" || origin == "" {
		return false
	}
	if _, ok := codexFirstPartyOriginators[origin]; !ok && !strings.HasPrefix(origin, "Codex ") {
		return false
	}
	prefix := origin + "/"
	if !strings.HasPrefix(ua, prefix) {
		return false
	}
	version := ua[len(prefix):]
	if idx := strings.IndexAny(version, " \t"); idx >= 0 {
		version = version[:idx]
	}
	return codexClientVersionPattern.MatchString(version)
}

// PreserveNativeCodexIdentity reports whether a coherent downstream Codex identity should be kept
// instead of being replaced by the built-in Codex identity. Unset configuration means enabled.
func PreserveNativeCodexIdentity(cfg *config.Config) bool {
	if cfg == nil || cfg.Codex.PreserveNativeClientIdentity == nil {
		return true
	}
	return *cfg.Codex.PreserveNativeClientIdentity
}
