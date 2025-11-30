package copilot

import (
	"errors"
	"fmt"
)

var (
	// ErrDeviceCodeFailed indicates failure to obtain a device code.
	ErrDeviceCodeFailed = errors.New("failed to get device code")

	// ErrAccessTokenFailed indicates failure to obtain an access token.
	ErrAccessTokenFailed = errors.New("failed to get access token")

	// ErrCopilotTokenFailed indicates failure to obtain a Copilot token.
	ErrCopilotTokenFailed = errors.New("failed to get Copilot token")

	// ErrTokenExpired indicates the Copilot token has expired.
	ErrTokenExpired = errors.New("copilot token has expired")

	// ErrNoGitHubToken indicates no GitHub token is available.
	ErrNoGitHubToken = errors.New("no GitHub token available")

	// ErrNoCopilotToken indicates no Copilot token is available.
	ErrNoCopilotToken = errors.New("no Copilot token available")

	// ErrAuthorizationPending indicates the user has not yet completed authorization.
	ErrAuthorizationPending = errors.New("authorization pending")

	// ErrSlowDown indicates the polling interval should be increased.
	ErrSlowDown = errors.New("slow down polling")

	// ErrAccessDenied indicates the user denied access.
	ErrAccessDenied = errors.New("access denied by user")

	// ErrExpiredToken indicates the device code has expired.
	ErrExpiredToken = errors.New("device code expired")

	// ErrNoCopilotSubscription indicates the user does not have a Copilot subscription.
	ErrNoCopilotSubscription = errors.New("no Copilot subscription found")
)

// HTTPStatusError wraps an error with an HTTP status code for structured error handling.
// This allows callers to inspect the status code without parsing error message strings.
type HTTPStatusError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *HTTPStatusError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("status %d: %s: %v", e.StatusCode, e.Message, e.Cause)
	}
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Message)
}

func (e *HTTPStatusError) Unwrap() error {
	return e.Cause
}

// NewHTTPStatusError creates a new HTTPStatusError with the given status code and message.
func NewHTTPStatusError(statusCode int, message string, cause error) *HTTPStatusError {
	return &HTTPStatusError{StatusCode: statusCode, Message: message, Cause: cause}
}

// StatusCode extracts the HTTP status code from an HTTPStatusError, or returns 0 if not applicable.
func StatusCode(err error) int {
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode
	}
	return 0
}
