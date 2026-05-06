package cliproxy

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// validateOAuthAliasExclusions returns human-readable warning strings for any
// (channel, alias) -> upstream_name mapping in `aliases` whose upstream_name
// matches any pattern in `excluded[channel]`. The match uses the same
// case-insensitive wildcard semantics as routing-time exclusion via
// matchWildcard.
//
// An alias that resolves to an excluded upstream silently fails at request
// time with a generic "unknown provider for model" error. This surface fires
// the warning at config-load and reload, before any request lands, so the
// misconfiguration is visible at the boundary instead of at every request.
//
// Returns nil if no conflicts. Caller logs each entry at WARN level.
//
// Channel names match provider names for the alias-supported set
// (vertex, claude, codex, antigravity, aistudio, gemini-cli, kimi); see
// auth.OAuthModelAliasChannel. Per-account excluded_models stored as auth
// attributes are not consulted here — they live at synthesizer time and need
// a separate hook.
func validateOAuthAliasExclusions(aliases map[string][]config.OAuthModelAlias, excluded map[string][]string) []string {
	if len(aliases) == 0 || len(excluded) == 0 {
		return nil
	}
	var warnings []string
	for rawChannel, entries := range aliases {
		channel := strings.ToLower(strings.TrimSpace(rawChannel))
		if channel == "" || len(entries) == 0 {
			continue
		}
		patterns := excluded[channel]
		if len(patterns) == 0 {
			continue
		}
		for _, entry := range entries {
			upstream := strings.TrimSpace(entry.Name)
			alias := strings.TrimSpace(entry.Alias)
			if upstream == "" || alias == "" {
				continue
			}
			// Mirror auth.compileOAuthModelAliasTable: self-aliases (upstream
			// equals alias, case-insensitive) never enter the runtime table,
			// so warning about them would be misleading.
			if strings.EqualFold(upstream, alias) {
				continue
			}
			upstreamLower := strings.ToLower(upstream)
			for _, pattern := range patterns {
				p := strings.TrimSpace(pattern)
				if p == "" {
					continue
				}
				if matchWildcard(strings.ToLower(p), upstreamLower) {
					warnings = append(warnings, fmt.Sprintf(
						"oauth-model-alias: alias=%q channel=%q upstream=%q matches provider-wide exclusion pattern=%q — alias will not resolve at runtime",
						alias, channel, upstream, p,
					))
				}
			}
		}
	}
	return warnings
}
