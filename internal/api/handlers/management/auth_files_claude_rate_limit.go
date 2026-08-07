package management

import (
	"strings"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// buildClaudeRateLimitEntry returns a structured view of the most-recent
// Anthropic `anthropic-ratelimit-unified-*` response-header state observed for
// this auth, suitable for embedding under a `rate_limit` key on an auth-files
// entry.
//
// The `windows` map is the exception to "most-recent": the store carries live
// windows forward, so each carries its own `observed_at` (see
// coreauth.SetAnthropicRateLimitHint).
//
// Returns nil when the auth is not a Claude provider, no hint has been
// captured yet, the hint has Known=false, or the captured hint belongs to a
// different account than this auth now holds. Mirrors extractCodexIDTokenClaims
// in shape and intent: provider-gated nested object, omitted entirely when
// there's no content to surface.
//
// The hint store is populated by the Claude executor (see
// internal/runtime/executor/helps/claude_rate_limit_record.go); this helper is
// a pure read-side projection.
func buildClaudeRateLimitEntry(auth *coreauth.Auth) gin.H {
	if auth == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return nil
	}
	hint, ok := coreauth.AnthropicRateLimitHintFor(auth)
	if !ok || !hint.Known {
		return nil
	}

	out := gin.H{}
	if !hint.ObservedAt.IsZero() {
		out["observed_at"] = hint.ObservedAt
	}
	if hint.Status != "" {
		out["status"] = hint.Status
	}
	if hint.RepresentativeClaim != "" {
		out["representative_claim"] = hint.RepresentativeClaim
	}
	if !hint.Reset.IsZero() {
		out["reset_at"] = hint.Reset
	}
	// Gate on the presence flag, not on != 0: a legitimate explicit 0 is a real
	// reading and must survive, while a malformed header must not surface as a
	// healthy-looking 0. Malformed values remain visible in raw_headers.
	if hint.HasFallbackPercentage {
		out["fallback_percentage"] = hint.FallbackPercentage
	}
	if hint.OverageStatus != "" {
		out["overage_status"] = hint.OverageStatus
	}
	if hint.OverageDisabledReason != "" {
		out["overage_disabled_reason"] = hint.OverageDisabledReason
	}
	if hint.UpgradePaths != "" {
		out["upgrade_paths"] = hint.UpgradePaths
	}

	if len(hint.Windows) > 0 {
		windows := make(gin.H, len(hint.Windows))
		for slug, window := range hint.Windows {
			windowEntry := gin.H{}
			if window.Status != "" {
				windowEntry["status"] = window.Status
			}
			if !window.Reset.IsZero() {
				windowEntry["reset_at"] = window.Reset
			}
			// Emit utilization only when the upstream actually shipped a
			// `unified-{slug}-utilization` header for this window. Without
			// this gate, an absent header would surface as 0.0 — indistinguishable
			// from a real zero-utilization reading and likely to mislead alerts
			// into treating unknown utilization as healthy usage.
			if window.HasUtilization {
				windowEntry["utilization"] = window.Utilization
			}
			if window.HasSurpassedThreshold {
				windowEntry["surpassed_threshold"] = window.SurpassedThreshold
			}
			// Skip windows where no field survived omitempty gating. A slug
			// whose only header was malformed (e.g. `unified-5h-reset: garbage`)
			// still seeds an entry in hint.Windows on the parser side; emitting
			// `"5h": {}` here would fabricate structured window data that tells
			// consumers nothing actionable and invites them to treat window
			// presence as meaningful. The forensic signal stays in raw_headers.
			if len(windowEntry) > 0 {
				// Which capture this window came from; a carried one lags the
				// top-level observed_at. After the emptiness gate on purpose:
				// a timestamp is not a reading and must not resurrect a window
				// with nothing to report.
				if !window.ObservedAt.IsZero() {
					windowEntry["observed_at"] = window.ObservedAt
				}
				windows[slug] = windowEntry
			}
		}
		// And drop the windows key entirely if every per-slug entry got
		// dropped — otherwise we'd surface `"windows": {}`. Mirrors the
		// top-level `if len(out) == 0` guard one level deeper.
		if len(windows) > 0 {
			out["windows"] = windows
		}
	}

	if len(hint.RawHeaders) > 0 {
		out["raw_headers"] = hint.RawHeaders
	}

	if len(out) == 0 {
		// No content survived omitempty gating; surface nothing rather than
		// an empty `rate_limit: {}` block.
		return nil
	}
	return out
}
