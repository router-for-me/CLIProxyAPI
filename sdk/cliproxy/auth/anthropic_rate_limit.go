package auth

import (
	"strings"
	"sync"
	"time"
)

// AnthropicRateLimitHint records the most-recent Anthropic
// `anthropic-ratelimit-unified-*` response-header state observed for one auth.
//
// This is passive observability state, populated by the Claude executor on every
// upstream response. It is NOT consulted by the conductor or selector for routing
// decisions — those continue to use Auth.Quota / Auth.NextRetryAfter. Operators
// (management API consumers, dashboards) read this hint to surface utilization
// and reset times before a credential is actually rate-limited.
//
// All fields except Known and ObservedAt are optional. Anthropic's
// `unified-*` family is undocumented and field set has been observed to grow
// over time (e.g. tier-specific 7d windows like `7d_opus`); the Windows map is
// keyed by header window slug rather than predeclared fields so new windows
// surface without code change.
type AnthropicRateLimitHint struct {
	// Known is true once any unified-* header has been observed for this auth.
	Known bool
	// ObservedAt is when the most recent capture happened (server clock).
	ObservedAt time.Time
	// Status mirrors `anthropic-ratelimit-unified-status`: e.g. "allowed",
	// "allowed_warning", "rejected". Pass-through string; do not enum-tighten.
	Status string
	// RepresentativeClaim names the binding window
	// (`anthropic-ratelimit-unified-representative-claim`); e.g. "five_hour",
	// "seven_day", "seven_day_opus". Pass-through string.
	RepresentativeClaim string
	// Reset is the reset moment of the representative window
	// (`anthropic-ratelimit-unified-reset`, epoch seconds → time.Time).
	Reset time.Time
	// Windows maps window slug → per-window state. Slugs are extracted from
	// header names of the form `anthropic-ratelimit-unified-{slug}-{field}`,
	// e.g. "5h", "7d", "7d_opus". The map is generative; consumers should
	// iterate rather than assume a fixed key set.
	Windows map[string]AnthropicQuotaWindow
	// FallbackPercentage mirrors `anthropic-ratelimit-unified-fallback-percentage`.
	// Optional; 0 when absent.
	FallbackPercentage float64
	// OverageStatus mirrors `anthropic-ratelimit-unified-overage-status`.
	// Optional; empty when absent.
	OverageStatus string
	// OverageDisabledReason mirrors
	// `anthropic-ratelimit-unified-overage-disabled-reason`. Optional.
	OverageDisabledReason string
	// UpgradePaths mirrors `anthropic-ratelimit-unified-upgrade-paths`. Optional.
	UpgradePaths string
	// RawHeaders preserves every observed `anthropic-ratelimit-unified-*` header
	// as a lower-cased name → first value map. Forward-compat safety net for
	// undocumented schema drift; may be nil when no headers were captured.
	RawHeaders map[string]string
}

// AnthropicQuotaWindow records per-window state captured from
// `anthropic-ratelimit-unified-{slug}-{field}` headers.
type AnthropicQuotaWindow struct {
	// Status mirrors `unified-{slug}-status`: "allowed", "allowed_warning",
	// "rejected". Pass-through string.
	Status string
	// Reset mirrors `unified-{slug}-reset` (epoch seconds → time.Time).
	Reset time.Time
	// Utilization mirrors `unified-{slug}-utilization` as a fraction. Can
	// exceed 1.0 when overage is in effect. Consult HasUtilization to
	// distinguish a real 0.0 reading from "header not present".
	Utilization float64
	// HasUtilization is true iff a `unified-{slug}-utilization` header was
	// observed for this window. Without this flag, a 0.0 utilization is
	// indistinguishable from an absent header — both cases would mislead
	// downstream alerts into treating unknown utilization as healthy usage.
	HasUtilization bool
	// SurpassedThreshold mirrors `unified-{slug}-surpassed-threshold`. Optional;
	// 0 when absent. Typically populated only when Status is at warning or above.
	SurpassedThreshold float64
}

var anthropicRateLimitHintByAuth sync.Map

// SetAnthropicRateLimitHint updates the latest known Anthropic rate-limit state
// for an auth. ObservedAt is defaulted to time.Now() if zero. Empty authID is
// silently ignored. Concurrent-safe.
func SetAnthropicRateLimitHint(authID string, hint AnthropicRateLimitHint) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if hint.ObservedAt.IsZero() {
		hint.ObservedAt = time.Now()
	}
	// Clone map fields so the stored hint owns its own state. Without this,
	// a caller that mutates the maps post-Set would corrupt the shared
	// store and risk "concurrent map iteration and map write" panics under
	// concurrent Get traffic. Pairs with the Get-side defensive copy: store
	// owns its inner maps, callers see independent copies on both sides.
	if hint.Windows != nil {
		cloned := make(map[string]AnthropicQuotaWindow, len(hint.Windows))
		for k, v := range hint.Windows {
			cloned[k] = v
		}
		hint.Windows = cloned
	}
	if hint.RawHeaders != nil {
		cloned := make(map[string]string, len(hint.RawHeaders))
		for k, v := range hint.RawHeaders {
			cloned[k] = v
		}
		hint.RawHeaders = cloned
	}
	anthropicRateLimitHintByAuth.Store(authID, hint)
}

// GetAnthropicRateLimitHint returns the latest known Anthropic rate-limit state
// for an auth. The returned bool is true when a hint has been stored for this
// authID at any point; the hint's Known field reflects whether the stored data
// includes any parsed unified-* header content (a non-empty record).
func GetAnthropicRateLimitHint(authID string) (AnthropicRateLimitHint, bool) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AnthropicRateLimitHint{}, false
	}
	value, ok := anthropicRateLimitHintByAuth.Load(authID)
	if !ok {
		return AnthropicRateLimitHint{}, false
	}
	hint, ok := value.(AnthropicRateLimitHint)
	if !ok {
		anthropicRateLimitHintByAuth.Delete(authID)
		return AnthropicRateLimitHint{}, false
	}
	// Clone map fields so callers cannot mutate internal state. Concurrent
	// readers each get an independent view; the shared store remains stable.
	// Without this, a caller mutating got.Windows or got.RawHeaders (even
	// accidentally while preparing response data) would race against other
	// readers and could trigger a `concurrent map read and map write` panic.
	if hint.Windows != nil {
		cloned := make(map[string]AnthropicQuotaWindow, len(hint.Windows))
		for k, v := range hint.Windows {
			cloned[k] = v
		}
		hint.Windows = cloned
	}
	if hint.RawHeaders != nil {
		cloned := make(map[string]string, len(hint.RawHeaders))
		for k, v := range hint.RawHeaders {
			cloned[k] = v
		}
		hint.RawHeaders = cloned
	}
	return hint, true
}

// HasKnownAnthropicRateLimitHint reports whether a hint with parsed content has
// been captured for this auth.
func HasKnownAnthropicRateLimitHint(authID string) bool {
	hint, ok := GetAnthropicRateLimitHint(authID)
	return ok && hint.Known
}

// DeleteAnthropicRateLimitHint removes any stored hint for an auth. Empty
// authID is a no-op. Concurrent-safe.
//
// Called from sdk/cliproxy.applyCoreAuthRemoval so a recreated auth with the
// same ID cannot surface stale quota state via the management API.
func DeleteAnthropicRateLimitHint(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	anthropicRateLimitHintByAuth.Delete(authID)
}
