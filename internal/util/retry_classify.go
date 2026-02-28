package util

import "strings"

// IsNonRetryableRefreshError reports whether a token refresh error is terminal
// and should not be retried immediately.
func IsNonRetryableRefreshError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refresh_token_reused") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "token_invalidated")
}
