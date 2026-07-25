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
// Conventions (kept in sync with executorKeyFromAuth):
//   - openai-compatibility              → OpenAICompatibleProviderKey(name)
//   - oauth:<channel>                   → channel (e.g. "claude", "codex")
//   - gemini-api-key / codex-api-key /  → the bare provider name lower-cased
//     xai-api-key / claude-api-key /
//     vertex-api-key / interactions-api-key
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
		return strings.ToLower(name)
	default:
		return strings.ToLower(name)
	}
}
