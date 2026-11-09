package auth

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// quotaWindow names the headers that describe one upstream usage window.
type quotaWindow struct {
	used       string
	resetAt    string
	resetAfter string
	// fraction is true when the provider reports usage as 0..1 instead of 0..100.
	fraction bool
}

var codexQuotaWindows = []quotaWindow{
	{used: "X-Codex-Primary-Used-Percent", resetAt: "X-Codex-Primary-Reset-At", resetAfter: "X-Codex-Primary-Reset-After-Seconds"},
	{used: "X-Codex-Secondary-Used-Percent", resetAt: "X-Codex-Secondary-Reset-At", resetAfter: "X-Codex-Secondary-Reset-After-Seconds"},
}

var claudeQuotaWindows = []quotaWindow{
	{used: "Anthropic-Ratelimit-Unified-5h-Utilization", resetAt: "Anthropic-Ratelimit-Unified-5h-Reset", fraction: true},
	{used: "Anthropic-Ratelimit-Unified-7d-Utilization", resetAt: "Anthropic-Ratelimit-Unified-7d-Reset", fraction: true},
}

// quotaWindowUsage reports the highest used percentage across the credential's
// 5-hour and weekly windows, read from the latest upstream response headers.
// A window whose reset time has passed, or that has no reset time, is ignored so
// an old reading cannot outlive its window. ok is false when no window applies.
func quotaWindowUsage(auth *Auth, now time.Time) (used float64, ok bool) {
	if auth == nil || len(auth.Quota.Signals) == 0 {
		return 0, false
	}
	var windows []quotaWindow
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "codex":
		windows = codexQuotaWindows
	case "claude":
		windows = claudeQuotaWindows
	default:
		return 0, false
	}
	for _, window := range windows {
		value, errParse := strconv.ParseFloat(strings.TrimSpace(auth.Quota.Signals[window.used]), 64)
		if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}
		if window.fraction {
			value *= 100
		}
		if reset := quotaWindowReset(auth.Quota, window); !reset.After(now) {
			continue
		}
		if !ok || value > used {
			used, ok = value, true
		}
	}
	return used, ok
}

func quotaWindowReset(quota QuotaState, window quotaWindow) time.Time {
	raw := strings.TrimSpace(quota.Signals[window.resetAt])
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil && seconds > 0 && !math.IsInf(seconds, 0) {
		return time.Unix(int64(seconds), 0)
	}
	if reset, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return reset
	}
	if window.resetAfter == "" || quota.ObservedAt.IsZero() {
		return time.Time{}
	}
	delay, errParse := strconv.ParseFloat(strings.TrimSpace(quota.Signals[window.resetAfter]), 64)
	if errParse != nil || !(delay > 0) || math.IsInf(delay, 0) {
		return time.Time{}
	}
	return quota.ObservedAt.Add(time.Duration(delay * float64(time.Second)))
}
