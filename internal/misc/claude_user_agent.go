package misc

import (
	"net/http"
	"strings"
)

// IsClaudeCompatibleUserAgent reports whether the User-Agent belongs to a
// Claude-compatible client or official Anthropic SDK.
func IsClaudeCompatibleUserAgent(userAgent string) bool {
	normalized := strings.ToLower(strings.TrimSpace(userAgent))
	if normalized == "" {
		return false
	}

	return strings.HasPrefix(normalized, "claude-cli") ||
		strings.HasPrefix(normalized, "claude-code") ||
		strings.HasPrefix(normalized, "claude code") ||
		strings.HasPrefix(normalized, "anthropic/")
}

// IsClaudeCompatibleHeaders reports whether request headers look like a
// Claude-compatible API request.
func IsClaudeCompatibleHeaders(headers http.Header) bool {
	if headers == nil {
		return false
	}
	if IsClaudeCompatibleUserAgent(headers.Get("User-Agent")) {
		return true
	}

	return strings.TrimSpace(headers.Get("X-Api-Key")) != "" ||
		strings.TrimSpace(headers.Get("Anthropic-Version")) != "" ||
		strings.TrimSpace(headers.Get("Anthropic-Beta")) != "" ||
		strings.TrimSpace(headers.Get("Anthropic-Dangerous-Direct-Browser-Access")) != ""
}
