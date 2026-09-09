package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ErrorCodeUpstreamAuthenticationRequired tells clients that retrying without
// operator reauthentication cannot recover the eligible upstream credentials.
const ErrorCodeUpstreamAuthenticationRequired = "upstream_authentication_required"

// Keep the auth_unavailable identity for existing selector callers, including
// scheduler snapshot refresh, while preserving a distinct client error body.
type upstreamAuthenticationRequiredError struct {
	*errorWithCause
}

func newUpstreamAuthenticationRequiredError(cause error) error {
	return &upstreamAuthenticationRequiredError{&errorWithCause{
		base: &Error{
			Code:       "auth_unavailable",
			Message:    "All eligible upstream credentials require reauthentication; ask the proxy operator to sign in again",
			HTTPStatus: http.StatusServiceUnavailable,
		},
		cause: cause,
	}}
}

func (e *upstreamAuthenticationRequiredError) Error() string {
	payload := map[string]any{"error": map[string]string{
		"type":    "server_error",
		"code":    ErrorCodeUpstreamAuthenticationRequired,
		"message": e.errorWithCause.Error(),
	}}
	// A payload containing only strings is always JSON-serializable.
	data, _ := json.Marshal(payload)
	return string(data)
}

// IsUpstreamAuthenticationRequired reports a terminal refresh failure across
// all eligible credentials. It does not classify a pool by its last error alone.
func IsUpstreamAuthenticationRequired(err error) bool {
	var required *upstreamAuthenticationRequiredError
	return errors.As(err, &required) && required != nil
}

func hasTerminalRefreshFailure(auth *Auth, now time.Time) bool {
	return auth != nil && !auth.Disabled && auth.Status != StatusDisabled &&
		auth.AuthKind() == AuthKindOAuth && hasUnauthorizedAuthFailure(auth) &&
		!auth.LastError.Retryable && auth.StatusMessage == "unauthorized" &&
		!auth.NextRetryAfter.After(now) && !auth.Quota.NextRecoverAt.After(now) &&
		!auth.HasValidAccessToken(now)
}

func allAuthsRequireReauthentication(auths []*Auth, now time.Time) bool {
	if len(auths) == 0 {
		return false
	}
	for _, candidate := range auths {
		if !hasTerminalRefreshFailure(candidate, now) {
			return false
		}
	}
	return true
}

func (m *modelScheduler) terminalRefreshFailureCountLocked(predicate func(*scheduledAuth) bool, now time.Time) int {
	count := 0
	for _, entry := range m.entries {
		if entry == nil || (predicate != nil && !predicate(entry)) {
			continue
		}
		if hasTerminalRefreshFailure(entry.auth, now) {
			count++
		}
	}
	return count
}
