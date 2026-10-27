package auth

import (
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	// AttributeQuotaWindowSeconds is the immutable credential attribute holding the
	// upstream quota window length in seconds (for example 18000 for Claude).
	AttributeQuotaWindowSeconds = "quota_window_seconds"
	// MetadataQuotaWindowAnchor is the runtime metadata key holding the RFC3339
	// timestamp at which the current quota window was first observed. It is
	// written once per credential by the result recording path.
	MetadataQuotaWindowAnchor = "quota_window_anchor"

	quotaSignalAnthropicUnifiedReset         = "anthropic-ratelimit-unified-reset"
	quotaSignalAnthropicReset                = "anthropic-ratelimit-reset"
	quotaSignalCodexPrimaryResetAt           = "x-codex-primary-reset-at"
	quotaSignalCodexPrimaryResetAfterSeconds = "x-codex-primary-reset-after-seconds"
)

// maxQuotaWindowSeconds bounds the derived window length so anchor.Add cannot
// overflow time.Duration.
const maxQuotaWindowSeconds = int64(math.MaxInt64 / int64(time.Second))

// QuotaResetTime returns the earliest usable reset instant encoded in the
// observed quota signals.
//
// Only reset headers relevant to provider are considered, in the order the
// upstream defines them as authoritative. Absolute values are Unix seconds and
// relative values are seconds from now. Values that cannot be parsed, that are
// not strictly in the future, or that would overflow a duration are ignored.
// The Signals map is never modified.
func (q QuotaState) QuotaResetTime(provider string, now time.Time) (time.Time, bool) {
	if len(q.Signals) == 0 {
		return time.Time{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	for _, header := range quotaResetHeadersForProvider(provider) {
		raw, ok := quotaSignalValue(q.Signals, header)
		if !ok {
			continue
		}
		var (
			reset time.Time
			valid bool
		)
		if strings.HasSuffix(header, "-after-seconds") {
			reset, valid = parseQuotaResetAfterSeconds(raw, now)
		} else {
			reset, valid = parseQuotaResetUnixSeconds(raw)
		}
		if !valid || !reset.After(now) {
			continue
		}
		return reset, true
	}
	return time.Time{}, false
}

// quotaResetHeadersForProvider returns the reset headers that can describe the
// provider's credential quota, most authoritative first.
func quotaResetHeadersForProvider(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic":
		return []string{quotaSignalAnthropicUnifiedReset, quotaSignalAnthropicReset}
	case "codex", "openai":
		return []string{quotaSignalCodexPrimaryResetAt, quotaSignalCodexPrimaryResetAfterSeconds}
	default:
		return []string{
			quotaSignalAnthropicUnifiedReset,
			quotaSignalCodexPrimaryResetAt,
			quotaSignalCodexPrimaryResetAfterSeconds,
			quotaSignalAnthropicReset,
		}
	}
}

// quotaSignalValue looks a signal up case-insensitively. Signals are normally
// stored under http.CanonicalHeaderKey, but accepting any casing keeps parsing
// stable for callers that construct the map directly.
func quotaSignalValue(signals map[string]string, name string) (string, bool) {
	if value, ok := signals[name]; ok {
		return value, true
	}
	for key, value := range signals {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}

func parseQuotaResetUnixSeconds(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	seconds, errParse := strconv.ParseInt(raw, 10, 64)
	if errParse != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func parseQuotaResetAfterSeconds(raw string, now time.Time) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	seconds, errParse := strconv.ParseInt(raw, 10, 64)
	if errParse != nil || seconds < 0 || seconds > maxQuotaWindowSeconds {
		return time.Time{}, false
	}
	return now.Add(time.Duration(seconds) * time.Second), true
}

// resetTimeFromQuota resolves an auth's next quota reset: response header
// signals first (precise), then the configured window anchor derivation.
func (a *Auth) resetTimeFromQuota(now time.Time) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	provider := executorKeyFromAuth(a)
	if provider == "" {
		provider = a.Provider
	}
	if reset, ok := a.Quota.QuotaResetTime(provider, now); ok {
		return reset, true
	}
	return a.quotaWindowResetTime(now)
}

// quotaWindowResetTime derives reset = anchor + window when a credential
// declares a window length and has already recorded its window anchor. A reset
// that is not strictly in the future is not usable for urgent scheduling.
func (a *Auth) quotaWindowResetTime(now time.Time) (time.Time, bool) {
	if a == nil || len(a.Attributes) == 0 || len(a.Metadata) == 0 {
		return time.Time{}, false
	}
	windowSeconds, okWindow := quotaWindowSeconds(a)
	if !okWindow {
		return time.Time{}, false
	}
	anchor, okAnchor := quotaWindowAnchor(a.Metadata)
	if !okAnchor {
		return time.Time{}, false
	}
	reset := anchor.Add(time.Duration(windowSeconds) * time.Second)
	if !reset.After(now) {
		return time.Time{}, false
	}
	return reset, true
}

func quotaWindowSeconds(a *Auth) (int64, bool) {
	if a == nil || len(a.Attributes) == 0 {
		return 0, false
	}
	raw := strings.TrimSpace(a.Attributes[AttributeQuotaWindowSeconds])
	if raw == "" {
		return 0, false
	}
	seconds, errParse := strconv.ParseInt(raw, 10, 64)
	if errParse != nil || seconds <= 0 || seconds > maxQuotaWindowSeconds {
		return 0, false
	}
	return seconds, true
}

func quotaWindowAnchor(metadata map[string]any) (time.Time, bool) {
	if len(metadata) == 0 {
		return time.Time{}, false
	}
	switch value := metadata[MetadataQuotaWindowAnchor].(type) {
	case time.Time:
		if value.IsZero() {
			return time.Time{}, false
		}
		return value, true
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return time.Time{}, false
		}
		parsed, errParse := time.Parse(time.RFC3339, trimmed)
		if errParse != nil || parsed.IsZero() {
			return time.Time{}, false
		}
		return parsed, true
	default:
		return time.Time{}, false
	}
}

// ensureQuotaWindowAnchor records the first successful generation call as the
// window anchor for credentials that declare a quota window. The anchor is
// never overwritten, so it remains the start of the current window.
func (a *Auth) ensureQuotaWindowAnchor(now time.Time) {
	if a == nil {
		return
	}
	if _, okWindow := quotaWindowSeconds(a); !okWindow {
		return
	}
	if _, exists := a.Metadata[MetadataQuotaWindowAnchor]; exists {
		return
	}
	if a.Metadata == nil {
		a.Metadata = make(map[string]any)
	}
	if now.IsZero() {
		now = time.Now()
	}
	a.Metadata[MetadataQuotaWindowAnchor] = now.UTC().Format(time.RFC3339)
}
