package management

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CooldownProviderSet builds a lookup set of provider keys currently in
// cooldown from the auth manager snapshot. Returns nil when the snapshot
// is empty so callers can use a nil map for the "no cooldowns" path
// (saves an allocation per request).
func CooldownProviderSet(snapshot []auth.CooldownStateRecord) map[string]bool {
	if len(snapshot) == 0 {
		return nil
	}
	out := make(map[string]bool, len(snapshot))
	for _, rec := range snapshot {
		key := strings.ToLower(strings.TrimSpace(rec.Provider))
		if key == "" {
			continue
		}
		out[key] = true
	}
	return out
}

// IsProviderRowLive is the server-side single source of truth for the
// picker LIVE filter. Mirrors
// web/dashboard/src/components/modelRouteProvider.js:providerKeyIsLive —
// if you change one, change both and extend both test suites.
//
// row is the provider_key from an upstream_provider row or model_routing
// priority entry (e.g. "claude:42" or "openai-compatibility:7:key-3").
// liveEvidence is the auth manager's current LiveProviderKeysForModel
// output (lowercase, trimmed) for the model being routed. cooldown is
// the provider-key → true map built by CooldownProviderSet.
//
// A row is LIVE when:
//  1. It is NOT in cooldown (cooldown check first so a cooled-down key
//     never shows as live regardless of live evidence), AND
//  2. It matches some live evidence exactly, OR
//  3. It is an OpenAI-Compatibility provider-level route (no ':' in the
//     key, prefix "openai-compatible-") and some live evidence is either
//     the same provider key OR an entry key under it
//     ("openai-compatible-foo:bar"). This preserves the legacy operator
//     pin "openai-compatible-foo" as live when any of its entries is
//     currently serving the model.
//
// Other bare channels (e.g. "claude") are NOT prefix-matched to compound
// keys: a bare legacy OAuth auth does not prove that every compound
// Claude row is serving the model. Mirrors the JS comment "matching it
// here would mark an unrelated row live."
func IsProviderRowLive(row string, liveEvidence []string, cooldown map[string]bool) bool {
	if row == "" {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(row))
	if key == "" {
		return false
	}
	if cooldown[key] {
		return false
	}
	evidence := make([]string, 0, len(liveEvidence))
	for _, k := range liveEvidence {
		ek := strings.ToLower(strings.TrimSpace(k))
		if ek == "" {
			continue
		}
		evidence = append(evidence, ek)
	}
	for _, k := range evidence {
		if k == key {
			return true
		}
	}
	// OpenAI-Compatibility provider-level fallback: a bare
	// "openai-compatible-foo" route is live when ANY of its entry keys
	// ("openai-compatible-foo:bar") is currently live. This does NOT
	// apply to compound routes like "claude:42" — those must match
	// exactly (see JS comment re: legacy/OAuth bare-channel disambiguation).
	const openaiPrefix = "openai-compatible-"
	if strings.HasPrefix(key, openaiPrefix) && !strings.Contains(key, ":") {
		prefix := key + ":"
		for _, k := range evidence {
			if strings.HasPrefix(k, prefix) {
				return true
			}
		}
	}
	return false
}
