package management

import (
	"strings"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// buildClaudeRateLimitEntry returns a structured view of the most-recent
// Anthropic `anthropic-ratelimit-unified-*` response-header state observed for
// this auth, suitable for embedding under a `rate_limit` key on an auth-files
// entry.
//
// Returns nil when the auth is not a Claude provider, no hint has been
// captured yet, or the hint has Known=false. Mirrors extractCodexIDTokenClaims
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
	hint, ok := coreauth.GetAnthropicRateLimitHint(auth.ID)
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
	if hint.FallbackPercentage != 0 {
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
			if window.SurpassedThreshold != 0 {
				windowEntry["surpassed_threshold"] = window.SurpassedThreshold
			}
			windows[slug] = windowEntry
		}
		out["windows"] = windows
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
