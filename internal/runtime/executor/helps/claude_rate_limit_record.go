package helps

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

// Retention budget for a single capture. The hint is held per auth ID until
// that auth's next response or a scrub, so its size is bounded here rather
// than by the response that produced it.
//
// Without these, one response sizes the hint: the slug pattern's `\d+` admits
// unboundedly many distinct windows, and neither map caps its entry count or
// value length. Nothing in the tree sets MaxResponseHeaderBytes or
// MaxHeaderListSize, so Go's 10 MiB defaults are the only ceiling — and a
// context-injected RoundTripper bypasses even that. `baseURL` comes from the
// credential and third-party Anthropic-compatible bases are explicitly
// supported (isAnthropicUpstreamBase exists to tell them apart), so the
// producer of these headers is not necessarily Anthropic over TLS.
//
// The limits sit far above real captures — observed responses carry well
// under 20 unified headers across at most a handful of windows — so a
// legitimate capture is never truncated. Excess is dropped rather than
// erroring: this is passive observability, and a partial hint beats none.
const (
	maxAnthropicRawHeaders   = 128
	maxAnthropicWindows      = 64
	maxAnthropicHeaderValLen = 1024
)

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
//
// Takes the auth rather than a bare ID so the capture can be tagged with the
// account fingerprint it came from; readers use that to reject a capture that
// belongs to a credential since rotated out from under this ID. A nil auth or
// an auth with a blank ID is a no-op.
func RecordAnthropicRateLimit(auth *cliproxyauth.Auth, headers http.Header, now time.Time) {
	if auth == nil || headers == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}

	raw := make(map[string]string)
	windows := make(map[string]cliproxyauth.AnthropicQuotaWindow)
	hint := cliproxyauth.AnthropicRateLimitHint{
		ObservedAt:         now,
		AccountFingerprint: cliproxyauth.AnthropicAccountFingerprint(auth),
	}

	for canonicalName, values := range headers {
		if len(values) == 0 {
			continue
		}
		lower := strings.ToLower(canonicalName)
		if !strings.HasPrefix(lower, anthropicRateLimitHeaderPrefix) {
			continue
		}
		if len(raw) >= maxAnthropicRawHeaders {
			continue
		}
		value := values[0]
		if len(value) > maxAnthropicHeaderValLen {
			value = value[:maxAnthropicHeaderValLen]
		}
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
		if f, ok := parseAnthropicFloat(value); ok {
			hint.FallbackPercentage = f
			hint.HasFallbackPercentage = true
		}
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
		window, seen := windows[slug]
		if !seen && len(windows) >= maxAnthropicWindows {
			// Budget spent on windows already recorded; this slug stays
			// raw-only. Fields for slugs we are already tracking still
			// apply, so a truncated capture keeps whole windows rather
			// than a mix of partial ones.
			return
		}
		switch fieldSuffix {
		case "-status":
			window.Status = value
		case "-reset":
			window.Reset = parseAnthropicEpochSeconds(value)
		case "-utilization":
			// Tag presence so a consumer can tell an explicit 0.0 reading from
			// no utilization signal at all. Set only when the value actually
			// parses: a malformed header would otherwise be published as a
			// healthy 0.0, which is the reading an operator is least able to
			// question. Malformed values stay visible in RawHeaders.
			if f, ok := parseAnthropicFloat(value); ok {
				window.Utilization = f
				window.HasUtilization = true
			}
		case "-surpassed-threshold":
			if f, ok := parseAnthropicFloat(value); ok {
				window.SurpassedThreshold = f
				window.HasSurpassedThreshold = true
			}
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
func parseAnthropicFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
