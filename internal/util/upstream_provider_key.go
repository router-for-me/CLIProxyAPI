package util

import "strings"

// UpstreamProviderKey derives the executor/registry provider key that
// corresponds to a normalized upstream_providers row, given its provider_type
// and name. This mirrors the runtime logic in
// sdk/cliproxy/auth.executorKeyFromAuth so the dashboard can offer the same
// identifier the proxy uses for auth selection when no live auth has been
// registered yet (otherwise the per-model routing picker showed only the
// providers that currently happened to be live — typically just a handful —
// and operators could not pin a model to an upstream that was disabled or
// mid-refresh).
//
// Conventions (kept in sync with executorKeyFromAuth + the auth synthesizer):
//   - openai-compatibility              → OpenAICompatibleProviderKey(name)
//   - oauth:<channel>                   → channel (e.g. "claude", "codex")
//   - gemini-api-key / codex-api-key /  → the fixed channel name lower-cased
//     xai-api-key / claude-api-key /     (NOT the row's `name` field, which is
//     vertex-api-key / interactions-api-key  a free-form label; the synthesizer
//     hard-codes `auth.Provider = "<channel>"` for these types).
//
// Empty/unknown types fall back to the lower-cased name, matching the
// executor's "return strings.ToLower(auth.Provider)" tail when the auth
// carries a non-empty Provider but no special-case branch fires.
func UpstreamProviderKey(providerType, name string) string {
	pt := strings.ToLower(strings.TrimSpace(providerType))
	name = strings.TrimSpace(name)
	switch {
	case pt == "" || pt == "openai-compatibility":
		return OpenAICompatibleProviderKey(name)
	case strings.HasPrefix(pt, "oauth:"):
		return strings.ToLower(strings.TrimPrefix(pt, "oauth:"))
	case strings.HasSuffix(pt, "-api-key") || strings.HasSuffix(pt, "-api"):
		// Built-in API key channels share a fixed executor/registry key —
		// the per-row `name` column is a free-form label and must NOT be
		// used here, otherwise the per-model routing picker can never
		// match the live provider key returned by registry.GetModelProviders
		// (which always uses the channel name, not the row label).
		channel := strings.TrimSuffix(strings.TrimSuffix(pt, "-api-key"), "-api")
		if channel == "" {
			return strings.ToLower(name)
		}
		return channel
	default:
		return strings.ToLower(name)
	}
}
