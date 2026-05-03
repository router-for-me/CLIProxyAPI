package misc

import "strings"

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
