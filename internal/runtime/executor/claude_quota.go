package executor

import (
	"net/http"
	"strconv"
	"time"
)

// ClaudeCodeQuotaInfo stores rate limit information from Claude Code API headers.
// These headers are returned on every request to the Claude API for OAuth accounts.
type ClaudeCodeQuotaInfo struct {
	// UnifiedStatus indicates overall rate limit status
	UnifiedStatus string `json:"unified_status,omitempty"`

	// FiveHourStatus indicates 5-hour window status
	FiveHourStatus string `json:"five_hour_status,omitempty"`
	// FiveHourReset is when the 5-hour quota resets (Unix timestamp)
	FiveHourReset int64 `json:"five_hour_reset,omitempty"`
	// FiveHourUtilization is the current utilization [0.0-1.0]
	FiveHourUtilization float64 `json:"five_hour_utilization,omitempty"`

	// SevenDayStatus indicates 7-day window status
	SevenDayStatus string `json:"seven_day_status,omitempty"`
	// SevenDayReset is when the 7-day quota resets (Unix timestamp)
	SevenDayReset int64 `json:"seven_day_reset,omitempty"`
	// SevenDayUtilization is the current utilization [0.0-1.0]
	SevenDayUtilization float64 `json:"seven_day_utilization,omitempty"`

	// OverageStatus indicates overage quota status
	OverageStatus string `json:"overage_status,omitempty"`
	// OverageReset is when the overage quota resets (Unix timestamp)
	OverageReset int64 `json:"overage_reset,omitempty"`
	// OverageUtilization is the current overage utilization
	OverageUtilization float64 `json:"overage_utilization,omitempty"`

	// RepresentativeClaim indicates which quota window is representative
	RepresentativeClaim string `json:"representative_claim,omitempty"`
	// FallbackPercentage is the fallback percentage available
	FallbackPercentage float64 `json:"fallback_percentage,omitempty"`
	// FallbackAvailable indicates if fallback is available
	FallbackAvailable string `json:"fallback_available,omitempty"`
	// UnifiedReset is the overall quota reset time (Unix timestamp)
	UnifiedReset int64 `json:"unified_reset,omitempty"`

	// LastUpdated is when this quota info was last refreshed
	LastUpdated time.Time `json:"last_updated"`
}

// parseClaudeCodeQuotaHeaders extracts rate limit information from HTTP response headers.
// Returns nil if no quota headers are present.
func parseClaudeCodeQuotaHeaders(headers http.Header) *ClaudeCodeQuotaInfo {
	if headers == nil {
		return nil
	}

	// Check if any quota headers are present
	if headers.Get("anthropic-ratelimit-unified-status") == "" {
		return nil
	}

	info := &ClaudeCodeQuotaInfo{
		UnifiedStatus:       headers.Get("anthropic-ratelimit-unified-status"),
		FiveHourStatus:      headers.Get("anthropic-ratelimit-unified-5h-status"),
		FiveHourReset:       parseUnixTimestamp(headers.Get("anthropic-ratelimit-unified-5h-reset")),
		FiveHourUtilization: parseFloat(headers.Get("anthropic-ratelimit-unified-5h-utilization")),
		SevenDayStatus:      headers.Get("anthropic-ratelimit-unified-7d-status"),
		SevenDayReset:       parseUnixTimestamp(headers.Get("anthropic-ratelimit-unified-7d-reset")),
		SevenDayUtilization: parseFloat(headers.Get("anthropic-ratelimit-unified-7d-utilization")),
		OverageStatus:       headers.Get("anthropic-ratelimit-unified-overage-status"),
		OverageReset:        parseUnixTimestamp(headers.Get("anthropic-ratelimit-unified-overage-reset")),
		OverageUtilization:  parseFloat(headers.Get("anthropic-ratelimit-unified-overage-utilization")),
		RepresentativeClaim: headers.Get("anthropic-ratelimit-unified-representative-claim"),
		FallbackPercentage:  parseFloat(headers.Get("anthropic-ratelimit-unified-fallback-percentage")),
		FallbackAvailable:   headers.Get("anthropic-ratelimit-unified-fallback"),
		UnifiedReset:        parseUnixTimestamp(headers.Get("anthropic-ratelimit-unified-reset")),
		LastUpdated:         time.Now().UTC(),
	}

	return info
}

// parseUnixTimestamp converts a string Unix timestamp to int64.
// Returns 0 if the string is empty or invalid.
func parseUnixTimestamp(s string) int64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

// parseFloat converts a string to float64.
// Returns 0 if the string is empty or invalid.
func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
