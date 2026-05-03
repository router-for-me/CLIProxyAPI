package helps

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

const anthropicRateLimitHeaderPrefix = "anthropic-ratelimit-unified-"

// Per-window field suffixes, ordered longest-first so that `-status` does not
// preempt `-surpassed-threshold` during suffix matching.
var anthropicPerWindowFieldSuffixes = []string{
	"-surpassed-threshold",
	"-utilization",
	"-status",
	"-reset",
}

// anthropicWindowSlugPattern constrains the per-window fallback to slugs that
// actually look like quota-window identifiers: <digits><h|d> with an optional
// `_<lowercase_word>` tier suffix (e.g. "5h", "7d", "7d_opus", "7d_sonnet").
//
// Without this gate, any future top-level header ending in one of the field
// suffixes (for example `unified-overage-reset` or a hypothetical
// `unified-foo-status`) would be misparsed into a synthetic windows[slug]
// entry, corrupting the structured rate_limit.windows view that management
// consumers depend on. Headers that fail this check stay raw-only — they are
// already captured upstream into RawHeaders, which is the forward-compat
// safety net for schema drift.
var anthropicWindowSlugPattern = regexp.MustCompile(`^\d+[hd](?:_[a-z_]+)?$`)

// RecordAnthropicRateLimit extracts the `anthropic-ratelimit-unified-*` family
// from an upstream Anthropic response and stashes the parsed state on the auth
// hint store (sdk/cliproxy/auth.SetAnthropicRateLimitHint).
//
// Called from the Claude executor after each upstream round-trip, regardless of
// status code. Pure passive observability — no error is returned, no routing
// behavior changes. The conductor and selector continue to consult Auth.Quota
// and Auth.NextRetryAfter exclusively for routing decisions.
//
// If the response carries no `unified-*` headers (raw API-key traffic, a
// response from a non-subscription endpoint, or the no-headers 429 regression
// reported in NousResearch/hermes-agent#17169), the hint store is left
// untouched — any prior hint stays put rather than being overwritten with
// empty state.
func RecordAnthropicRateLimit(authID string, headers http.Header, now time.Time) {
	authID = strings.TrimSpace(authID)
	if authID == "" || headers == nil {
		return
	}

	raw := make(map[string]string)
	windows := make(map[string]cliproxyauth.AnthropicQuotaWindow)
	hint := cliproxyauth.AnthropicRateLimitHint{
		ObservedAt: now,
	}

	for canonicalName, values := range headers {
		if len(values) == 0 {
			continue
		}
		lower := strings.ToLower(canonicalName)
		if !strings.HasPrefix(lower, anthropicRateLimitHeaderPrefix) {
			continue
		}
		value := values[0]
		raw[lower] = value
		suffix := strings.TrimPrefix(lower, anthropicRateLimitHeaderPrefix)
		recordAnthropicRateLimitField(&hint, windows, suffix, value)
	}

	// No `unified-*` headers seen anywhere; leave any prior hint intact.
	// We commit a hint only when the upstream response actually carried our
	// signal — the alternative (overwrite with empty state on every claude
	// response) would erase prior captures whenever a non-Anthropic-shaped
	// reply slipped through this executor or the family was stripped from a
	// 429 response (cf. NousResearch/hermes-agent#17169).
	if len(raw) == 0 {
		return
	}

	hint.Known = true
	hint.RawHeaders = raw
	if len(windows) > 0 {
		hint.Windows = windows
	}
	cliproxyauth.SetAnthropicRateLimitHint(authID, hint)
}

// recordAnthropicRateLimitField routes a single header (already stripped of
// the `anthropic-ratelimit-unified-` prefix) into either a top-level hint slot
// or a per-window slot. Unknown suffixes are silently ignored at this layer;
// they remain accessible via the parent hint's RawHeaders map.
func recordAnthropicRateLimitField(
	hint *cliproxyauth.AnthropicRateLimitHint,
	windows map[string]cliproxyauth.AnthropicQuotaWindow,
	suffix, value string,
) {
	switch suffix {
	case "status":
		hint.Status = value
		return
	case "representative-claim":
		hint.RepresentativeClaim = value
		return
	case "reset":
		hint.Reset = parseAnthropicEpochSeconds(value)
		return
	case "fallback-percentage":
		hint.FallbackPercentage = parseAnthropicFloat(value)
		return
	case "overage-status":
		hint.OverageStatus = value
		return
	case "overage-disabled-reason":
		hint.OverageDisabledReason = value
		return
	case "upgrade-paths":
		hint.UpgradePaths = value
		return
	}

	for _, fieldSuffix := range anthropicPerWindowFieldSuffixes {
		if !strings.HasSuffix(suffix, fieldSuffix) {
			continue
		}
		slug := strings.TrimSuffix(suffix, fieldSuffix)
		if slug == "" || !anthropicWindowSlugPattern.MatchString(slug) {
			// Looks like a per-window suffix but slug doesn't match the
			// window-slug pattern — likely a future top-level field
			// (e.g. unified-overage-reset → slug "overage"). Leave it in
			// RawHeaders (already captured upstream) and skip structured
			// parsing. Fail-open to RawHeaders preserves the
			// forward-compat safety net for schema drift.
			return
		}
		window := windows[slug]
		switch fieldSuffix {
		case "-status":
			window.Status = value
		case "-reset":
			window.Reset = parseAnthropicEpochSeconds(value)
		case "-utilization":
			window.Utilization = parseAnthropicFloat(value)
			// Tag presence so the serializer can omit the field when the
			// upstream response had no utilization signal at all (vs. an
			// explicit 0.0 reading). Stays true even when parseAnthropicFloat
			// falls back to 0 on a malformed value: the operator-visible
			// signal is still "Anthropic shipped this header", and 0 is the
			// safest fallback consistent with how status/reset behave on
			// malformed input.
			window.HasUtilization = true
		case "-surpassed-threshold":
			window.SurpassedThreshold = parseAnthropicFloat(value)
		}
		windows[slug] = window
		return
	}
}

// parseAnthropicEpochSeconds parses an epoch-seconds header value (Anthropic's
// `reset` field format) into UTC time.Time. Returns the zero time on any parse
// failure — callers should treat zero as "unknown" via .IsZero().
//
// Epoch values that fall outside the year range [0001, 9999] are also rejected
// (returns zero). time.Time.MarshalJSON refuses to serialize times outside that
// range, so a malicious upstream sending e.g. `99999999999999` would otherwise
// stash a year-5138+ timestamp on the auth hint and crash any management
// endpoint that JSON-marshals the AnthropicRateLimitHint.
func parseAnthropicEpochSeconds(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	// JSON-serializable epoch range: [0001-01-01T00:00:00Z, 9999-12-31T23:59:59Z].
	const minEpoch = -62135596800
	const maxEpoch = 253402300799
	if n < minEpoch || n > maxEpoch {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// parseAnthropicFloat parses a float header value (utilization, threshold,
// fallback-percentage). Returns 0 on parse failure or for non-finite values
// (NaN, ±Inf) — strconv.ParseFloat accepts those literals, but storing them
// breaks downstream JSON serialization and any consumer arithmetic. Callers
// should not special-case 0 as "missing"; use the parent struct's presence
// to disambiguate.
func parseAnthropicFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
