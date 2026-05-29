package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Anthropic unified rate-limit response headers (returned on every response for
// Max/subscription "unified" plans). These expose the rolling 5-hour and 7-day
// usage windows so the proxy can proactively stop routing to an account before
// it trips the hard limit. Captured from a live OAuth response, e.g.:
//
//	Anthropic-Ratelimit-Unified-5h-Utilization: 0.25   (fraction 0..1 of the window used)
//	Anthropic-Ratelimit-Unified-5h-Reset: 1780084200   (unix seconds when the window resets)
//	Anthropic-Ratelimit-Unified-5h-Status: allowed|allowed_warning|rejected
//	Anthropic-Ratelimit-Unified-7d-Utilization / -7d-Reset / -7d-Status
//	Anthropic-Ratelimit-Unified-Status / -Reset (overall, representative window)
const (
	hdrUnified5hUtilization = "Anthropic-Ratelimit-Unified-5h-Utilization"
	hdrUnified5hReset       = "Anthropic-Ratelimit-Unified-5h-Reset"
	hdrUnified5hStatus      = "Anthropic-Ratelimit-Unified-5h-Status"
	hdrUnified7dUtilization = "Anthropic-Ratelimit-Unified-7d-Utilization"
	hdrUnified7dReset       = "Anthropic-Ratelimit-Unified-7d-Reset"
	hdrUnified7dStatus      = "Anthropic-Ratelimit-Unified-7d-Status"
	hdrUnifiedStatus        = "Anthropic-Ratelimit-Unified-Status"
	hdrUnifiedReset         = "Anthropic-Ratelimit-Unified-Reset"
)

// parseUnifiedReset parses a unified reset header. The unified headers use unix
// seconds (e.g. "1780084200"); RFC 3339 is accepted as a fallback for safety.
func parseUnifiedReset(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if secs <= 0 {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// windowBreached reports whether a single unified window should cause avoidance,
// based on its status and utilization against the threshold. A "rejected" status
// always counts. A positive threshold (fraction in (0,1]) triggers proactively
// once utilization reaches it.
func windowBreached(status, utilization string, threshold float64) bool {
	if strings.EqualFold(strings.TrimSpace(status), "rejected") {
		return true
	}
	if threshold <= 0 {
		return false
	}
	util, err := strconv.ParseFloat(strings.TrimSpace(utilization), 64)
	if err != nil {
		return false
	}
	return util > 0 && util >= threshold
}

// claudeUsageParkUntil inspects Anthropic unified rate-limit headers and returns
// the time until which the account should be parked (not routed to), or the zero
// time when no window is breached. When multiple windows are breached it returns
// the latest reset, since the account stays limited until every breached window
// recovers. threshold is the utilization fraction (e.g. 0.85) at which to start
// avoiding; <= 0 disables proactive avoidance (only hard "rejected" triggers).
func claudeUsageParkUntil(h http.Header, threshold float64, now time.Time) time.Time {
	if h == nil {
		return time.Time{}
	}
	var until time.Time
	consider := func(statusKey, utilKey, resetKey string) {
		if !windowBreached(h.Get(statusKey), h.Get(utilKey), threshold) {
			return
		}
		reset, ok := parseUnifiedReset(h.Get(resetKey))
		if !ok || !reset.After(now) {
			return
		}
		if reset.After(until) {
			until = reset
		}
	}
	consider(hdrUnified5hStatus, hdrUnified5hUtilization, hdrUnified5hReset)
	consider(hdrUnified7dStatus, hdrUnified7dUtilization, hdrUnified7dReset)
	// Overall status has no utilization field; only the hard "rejected" matters.
	if strings.EqualFold(strings.TrimSpace(h.Get(hdrUnifiedStatus)), "rejected") {
		if reset, ok := parseUnifiedReset(h.Get(hdrUnifiedReset)); ok && reset.After(now) && reset.After(until) {
			until = reset
		}
	}
	return until
}
