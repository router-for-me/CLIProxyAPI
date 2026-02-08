package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// QuotaInfo holds extracted quota data from provider API responses.
// It captures quota information from either HTTP headers or JSON response bodies,
// depending on the provider's reporting format.
type QuotaInfo struct {
	// Used is the number of requests or tokens consumed in the current quota window.
	Used int64
	// Remaining is the number of requests or tokens remaining in the current quota window.
	Remaining int64
	// Limit is the maximum quota allowed in the current window.
	Limit int64
	// ResetAt is when the quota window resets and quota becomes available again.
	ResetAt time.Time
	// Exceeded indicates whether the quota limit has been hit (typically from 429 status).
	Exceeded bool
	// Source indicates where the quota data came from ("header" or "body").
	Source string
}

// ExtractClaudeQuota parses quota information from Anthropic Claude API response headers.
// Claude uses anthropic-ratelimit-* headers to report quota state.
//
// Headers parsed:
//   - anthropic-ratelimit-requests-remaining: Number of requests remaining
//   - anthropic-ratelimit-requests-limit: Maximum requests allowed
//   - anthropic-ratelimit-requests-reset: RFC3339 timestamp when quota resets
//
// Returns nil if no quota headers are found (graceful handling).
// Sets Exceeded=true if statusCode is 429 (rate limit error).
func ExtractClaudeQuota(headers http.Header, statusCode int) *QuotaInfo {
	// Check if any quota headers are present
	if headers.Get("anthropic-ratelimit-requests-remaining") == "" &&
		headers.Get("anthropic-ratelimit-requests-limit") == "" {
		return nil // No quota information available
	}

	remaining := parseHeaderInt64(headers, "anthropic-ratelimit-requests-remaining")
	limit := parseHeaderInt64(headers, "anthropic-ratelimit-requests-limit")
	resetTime := parseHeaderTime(headers, "anthropic-ratelimit-requests-reset")

	return &QuotaInfo{
		Remaining: remaining,
		Limit:     limit,
		ResetAt:   resetTime,
		Exceeded:  statusCode == 429,
		Source:    "header",
	}
}

// ExtractGeminiQuota parses quota information from Google Gemini API response body.
// Gemini may include usageMetadata in some responses, though quota reporting
// varies by response type and API endpoint.
//
// JSON paths parsed:
//   - usageMetadata.quotaRemaining: Remaining quota units
//   - usageMetadata.quotaLimit: Maximum quota units
//
// Returns nil if usageMetadata is not present in the response (graceful handling).
// Sets Exceeded=true if statusCode is 429 (rate limit error).
//
// Note: Gemini does not consistently expose quota in all responses. This handles
// cases where quota data is available.
func ExtractGeminiQuota(body []byte, statusCode int) *QuotaInfo {
	// Check if usageMetadata exists in the response
	if !gjson.GetBytes(body, "usageMetadata").Exists() {
		return nil // No quota information available
	}

	// Note: These field names are illustrative. Actual Gemini quota fields
	// may differ. This implementation should be verified against real API responses.
	remaining := gjson.GetBytes(body, "usageMetadata.quotaRemaining").Int()
	limit := gjson.GetBytes(body, "usageMetadata.quotaLimit").Int()

	// Only return QuotaInfo if we found actual quota data
	if remaining == 0 && limit == 0 {
		return nil
	}

	return &QuotaInfo{
		Remaining: remaining,
		Limit:     limit,
		ResetAt:   time.Time{}, // Gemini may not provide reset time in body
		Exceeded:  statusCode == 429,
		Source:    "body",
	}
}

// parseHeaderInt64 safely parses an HTTP header value as int64.
// Returns 0 if the header is missing or cannot be parsed (graceful fallback).
func parseHeaderInt64(headers http.Header, key string) int64 {
	value := strings.TrimSpace(headers.Get(key))
	if value == "" {
		return 0
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0 // Graceful fallback on parse error
	}

	return parsed
}

// parseHeaderTime safely parses an HTTP header value as a time.Time.
// Expects RFC3339 format (e.g., "2026-02-06T12:00:00Z").
// Returns zero time if the header is missing or cannot be parsed (graceful fallback).
func parseHeaderTime(headers http.Header, key string) time.Time {
	value := strings.TrimSpace(headers.Get(key))
	if value == "" {
		return time.Time{}
	}

	// Try RFC3339 format first (standard for API timestamps)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		// Try RFC3339Nano as fallback
		parsed, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return time.Time{} // Graceful fallback on parse error
		}
	}

	return parsed
}
