package util

import (
	"strconv"
	"strings"
)

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
// rowID disambiguates built-in api-key rows so each upstream maps to a unique
// routing key (e.g. "claude:42") even when several rows share the same
// channel. The runtime still resolves all compound keys back to the same
// shared channel executor (see routingKeyFromAuth / executorKeyFromAuth).
//
// Conventions (kept in sync with executorKeyFromAuth + the auth synthesizer):
//   - openai-compatibility              → OpenAICompatibleProviderKey(name)
//   - oauth:<channel>                   → channel (e.g. "claude", "codex")
//   - gemini-api-key / codex-api-key /  → <channel>:<rowID> when rowID>0
//     xai-api-key / claude-api-key /     (so each row has its own routing key);
//     vertex-api-key / interactions-api-key → <channel> when rowID==0 (legacy)
//
// Empty/unknown types fall back to the lower-cased name, matching the
// executor's "return strings.ToLower(auth.Provider)" tail when the auth
// carries a non-empty Provider but no special-case branch fires.
func UpstreamProviderKey(providerType, name string, rowID int64) string {
	pt := strings.ToLower(strings.TrimSpace(providerType))
	name = strings.TrimSpace(name)
	switch {
	case pt == "" || pt == "openai-compatibility":
		return OpenAICompatibleProviderKey(name)
	case strings.HasPrefix(pt, "oauth:"):
		return strings.ToLower(strings.TrimPrefix(pt, "oauth:"))
	case strings.HasSuffix(pt, "-api-key") || strings.HasSuffix(pt, "-api"):
		// Built-in API key channels share a single executor (the runtime's
		// executor manager registers one executor per channel), so the
		// executor key is the channel. But for routing/filter purposes we
		// want each row to be individually selectable, so we suffix the
		// row's stable database id. rowID==0 yields the legacy bare
		// channel key — used by callers that have no row id (e.g. some
		// tests / seed code) and want the previous collapse behaviour.
		channel := strings.TrimSuffix(strings.TrimSuffix(pt, "-api-key"), "-api")
		// The interactions executor is registered as "gemini-interactions"
		// (not "interactions") so it can share the gemini family without
		// colliding with the bare channel name. Mirror that here.
		if channel == "interactions" {
			channel = "gemini-interactions"
		}
		if channel == "" {
			return strings.ToLower(name)
		}
		if rowID <= 0 {
			return channel
		}
		return channel + ":" + strconv.FormatInt(rowID, 10)
	default:
		return strings.ToLower(name)
	}
}
