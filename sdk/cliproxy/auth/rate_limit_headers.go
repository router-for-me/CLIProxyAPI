package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Anthropic sends these headers on every Claude Messages API response (including
// non-2xx) to report rolling rate-limit utilization for the credential.
const (
	claudeRateLimit5hUtilizationHeader = "Anthropic-Ratelimit-Unified-5h-Utilization"
	claudeRateLimit5hResetHeader       = "Anthropic-Ratelimit-Unified-5h-Reset"
	claudeRateLimit7dUtilizationHeader = "Anthropic-Ratelimit-Unified-7d-Utilization"
	claudeRateLimit7dResetHeader       = "Anthropic-Ratelimit-Unified-7d-Reset"
	claudeRateLimit7dStatusHeader      = "Anthropic-Ratelimit-Unified-7d-Status"
)

// ChatGPT/Codex sends these headers on /backend-api/codex/responses responses to
// report the primary (weekly) and secondary (short window, e.g. 5h) usage windows
// for the credential.
const (
	codexRateLimitPrimaryUsedPercentHeader          = "X-Codex-Primary-Used-Percent"
	codexRateLimitPrimaryWindowMinutesHeader        = "X-Codex-Primary-Window-Minutes"
	codexRateLimitPrimaryResetAfterSecondsHeader    = "X-Codex-Primary-Reset-After-Seconds"
	codexRateLimitPrimaryResetAtHeader              = "X-Codex-Primary-Reset-At"
	codexRateLimitSecondaryUsedPercentHeader        = "X-Codex-Secondary-Used-Percent"
	codexRateLimitSecondaryWindowMinutesHeader      = "X-Codex-Secondary-Window-Minutes"
	codexRateLimitSecondaryResetAfterSecondsHeader  = "X-Codex-Secondary-Reset-After-Seconds"
	codexRateLimitSecondaryResetAtHeader            = "X-Codex-Secondary-Reset-At"
	codexRateLimitPrimaryOverSecondaryPercentHeader = "X-Codex-Primary-Over-Secondary-Limit-Percent"
	codexSafetyBufferingEnabledHeader               = "X-Codex-Safety-Buffering-Enabled"
	codexSafetyBufferingFasterModelHeader           = "X-Codex-Safety-Buffering-Faster-Model"
)

// applyRateLimitHeaders extracts provider specific usage/rate-limit headers from an
// upstream response and caches a snapshot on auth.RateLimits (in-memory only, not
// persisted — see the field doc on Auth). It reports whether the cache was updated.
//
// observedAt marks when the response was received. Concurrent requests against the
// same auth can complete out of order relative to when they were sent, so an update
// is only applied when observedAt is at least as new as the previously stored
// snapshot; this prevents a slower in-flight request from clobbering a fresher one.
// The comparison and write happen while the caller (Manager.MarkResult) holds the
// auth lock, so no additional synchronization is required here.
func applyRateLimitHeaders(auth *Auth, headers http.Header, observedAt time.Time) bool {
	if auth == nil || len(headers) == 0 {
		return false
	}
	snapshot, ok := parseRateLimitHeaders(auth.Provider, headers)
	if !ok {
		return false
	}
	if existingObservedAt, okObserved := parseRateLimitObservedAt(auth.RateLimits); okObserved && observedAt.Before(existingObservedAt) {
		return false
	}
	snapshot["observed_at"] = observedAt.UTC().Format(time.RFC3339Nano)
	auth.RateLimits = snapshot
	return true
}

// parseRateLimitObservedAt reads the observed_at timestamp from a previously stored
// rate-limit snapshot. It reports ok=false when absent or malformed, in which case
// callers should treat the snapshot as having no ordering constraint.
func parseRateLimitObservedAt(snapshot map[string]any) (time.Time, bool) {
	raw, ok := snapshot["observed_at"].(string)
	if !ok || raw == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// parseRateLimitHeaders dispatches to a provider specific header parser. It reports
// ok=false when the provider has no known usage headers or none of them are present
// on this response.
func parseRateLimitHeaders(provider string, headers http.Header) (map[string]any, bool) {
	switch provider {
	case "claude":
		return parseClaudeRateLimitHeaders(headers)
	case "codex":
		return parseCodexRateLimitHeaders(headers)
	default:
		return nil, false
	}
}

func parseClaudeRateLimitHeaders(headers http.Header) (map[string]any, bool) {
	snapshot := make(map[string]any)
	setRateLimitInt(snapshot, "5h_utilization", headers.Get(claudeRateLimit5hUtilizationHeader))
	setRateLimitResetTime(snapshot, "5h_reset", headers.Get(claudeRateLimit5hResetHeader))
	setRateLimitInt(snapshot, "7d_utilization", headers.Get(claudeRateLimit7dUtilizationHeader))
	setRateLimitResetTime(snapshot, "7d_reset", headers.Get(claudeRateLimit7dResetHeader))
	if status := strings.TrimSpace(headers.Get(claudeRateLimit7dStatusHeader)); status != "" {
		snapshot["7d_status"] = status
	}
	if len(snapshot) == 0 {
		return nil, false
	}
	return snapshot, true
}

// parseCodexRateLimitHeaders reads the primary (typically weekly) and secondary
// (typically 5h) usage windows Codex reports on /responses. The window duration is
// data-driven via the *-window-minutes headers rather than assumed, since OpenAI
// controls how long each window is.
func parseCodexRateLimitHeaders(headers http.Header) (map[string]any, bool) {
	snapshot := make(map[string]any)
	setRateLimitInt(snapshot, "primary_used_percent", headers.Get(codexRateLimitPrimaryUsedPercentHeader))
	setRateLimitInt(snapshot, "primary_window_minutes", headers.Get(codexRateLimitPrimaryWindowMinutesHeader))
	setRateLimitInt(snapshot, "primary_reset_after_seconds", headers.Get(codexRateLimitPrimaryResetAfterSecondsHeader))
	setRateLimitResetTime(snapshot, "primary_reset_at", headers.Get(codexRateLimitPrimaryResetAtHeader))
	setRateLimitInt(snapshot, "secondary_used_percent", headers.Get(codexRateLimitSecondaryUsedPercentHeader))
	setRateLimitInt(snapshot, "secondary_window_minutes", headers.Get(codexRateLimitSecondaryWindowMinutesHeader))
	setRateLimitInt(snapshot, "secondary_reset_after_seconds", headers.Get(codexRateLimitSecondaryResetAfterSecondsHeader))
	setRateLimitResetTime(snapshot, "secondary_reset_at", headers.Get(codexRateLimitSecondaryResetAtHeader))
	setRateLimitInt(snapshot, "primary_over_secondary_limit_percent", headers.Get(codexRateLimitPrimaryOverSecondaryPercentHeader))
	setRateLimitBool(snapshot, "safety_buffering_enabled", headers.Get(codexSafetyBufferingEnabledHeader))
	if fasterModel := strings.TrimSpace(headers.Get(codexSafetyBufferingFasterModelHeader)); fasterModel != "" {
		snapshot["safety_buffering_faster_model"] = fasterModel
	}
	if len(snapshot) == 0 {
		return nil, false
	}
	return snapshot, true
}

// setRateLimitInt stores a numeric header value as an int when possible, falling
// back to the raw string so unexpected upstream formats still round-trip.
func setRateLimitInt(snapshot map[string]any, key, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if n, err := strconv.Atoi(raw); err == nil {
		snapshot[key] = n
		return
	}
	snapshot[key] = raw
}

// setRateLimitBool stores a boolean header value as a bool when possible, falling
// back to the raw string so unexpected upstream formats still round-trip.
func setRateLimitBool(snapshot map[string]any, key, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if b, err := strconv.ParseBool(raw); err == nil {
		snapshot[key] = b
		return
	}
	snapshot[key] = raw
}

// setRateLimitResetTime converts a Unix-seconds timestamp header value into RFC3339,
// falling back to the raw string when it is not a Unix timestamp.
func setRateLimitResetTime(snapshot map[string]any, key, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		snapshot[key] = time.Unix(sec, 0).UTC().Format(time.RFC3339)
		return
	}
	snapshot[key] = raw
}
