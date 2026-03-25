package codebuddy

import "errors"

var (
	ErrPollingTimeout   = errors.New("codebuddy: polling timeout, user did not authorize in time")
	ErrAccessDenied     = errors.New("codebuddy: access denied by user")
	ErrTokenFetchFailed = errors.New("codebuddy: failed to fetch token from server")
	ErrJWTDecodeFailed  = errors.New("codebuddy: failed to decode JWT token")
)

func GetUserFriendlyMessage(err error) string {
	switch {
	case errors.Is(err, ErrPollingTimeout):
		return "Authentication timed out. Please try again."
	case errors.Is(err, ErrAccessDenied):
		return "Access denied. Please try again and approve the login request."
	case errors.Is(err, ErrJWTDecodeFailed):
		return "Failed to decode token. Please try logging in again."
	case errors.Is(err, ErrTokenFetchFailed):
		return "Failed to fetch token from server. Please try again."
	default:
		return "Authentication failed: " + err.Error()
	}
}
