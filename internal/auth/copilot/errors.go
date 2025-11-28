package copilot

import (
	"errors"
	"fmt"
	"net/http"
)

// OAuthError represents an OAuth-specific error.
type OAuthError struct {
	// Code is the OAuth error code.
	Code string `json:"error"`
	// Description is a human-readable description of the error.
	Description string `json:"error_description,omitempty"`
	// URI is a URI identifying a human-readable web page with information about the error.
	URI string `json:"error_uri,omitempty"`
	// StatusCode is the HTTP status code associated with the error.
	StatusCode int `json:"-"`
}

// Error returns a string representation of the OAuth error.
func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("OAuth error %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("OAuth error: %s", e.Code)
}

// NewOAuthError creates a new OAuth error with the specified code, description, and status code.
func NewOAuthError(code, description string, statusCode int) *OAuthError {
	return &OAuthError{
		Code:        code,
		Description: description,
		StatusCode:  statusCode,
	}
}

// AuthenticationError represents authentication-related errors.
type AuthenticationError struct {
	// Type is the type of authentication error.
	Type string `json:"type"`
	// Message is a human-readable message describing the error.
	Message string `json:"message"`
	// Code is the HTTP status code associated with the error.
	Code int `json:"code"`
	// Cause is the underlying error that caused this authentication error.
	Cause error `json:"-"`
}

// Error returns a string representation of the authentication error.
func (e *AuthenticationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Unwrap returns the underlying cause of the error.
func (e *AuthenticationError) Unwrap() error {
	return e.Cause
}

// Common authentication error types for GitHub Copilot device flow.
var (
	// ErrDeviceCodeFailed represents an error when requesting the device code fails.
	ErrDeviceCodeFailed = &AuthenticationError{
		Type:    "device_code_failed",
		Message: "Failed to request device code from GitHub",
		Code:    http.StatusBadRequest,
	}

	// ErrDeviceCodeExpired represents an error when the device code has expired.
	ErrDeviceCodeExpired = &AuthenticationError{
		Type:    "device_code_expired",
		Message: "Device code has expired. Please try again.",
		Code:    http.StatusGone,
	}

	// ErrAuthorizationPending represents a pending authorization state (not an error, used for polling).
	ErrAuthorizationPending = &AuthenticationError{
		Type:    "authorization_pending",
		Message: "Authorization is pending. Waiting for user to authorize.",
		Code:    http.StatusAccepted,
	}

	// ErrSlowDown represents a request to slow down polling.
	ErrSlowDown = &AuthenticationError{
		Type:    "slow_down",
		Message: "Polling too frequently. Slowing down.",
		Code:    http.StatusTooManyRequests,
	}

	// ErrAccessDenied represents an error when the user denies authorization.
	ErrAccessDenied = &AuthenticationError{
		Type:    "access_denied",
		Message: "User denied authorization",
		Code:    http.StatusForbidden,
	}

	// ErrTokenExchangeFailed represents an error when token exchange fails.
	ErrTokenExchangeFailed = &AuthenticationError{
		Type:    "token_exchange_failed",
		Message: "Failed to exchange device code for access token",
		Code:    http.StatusBadRequest,
	}

	// ErrPollingTimeout represents an error when polling times out.
	ErrPollingTimeout = &AuthenticationError{
		Type:    "polling_timeout",
		Message: "Timeout waiting for user authorization",
		Code:    http.StatusRequestTimeout,
	}

	// ErrUserInfoFailed represents an error when fetching user info fails.
	ErrUserInfoFailed = &AuthenticationError{
		Type:    "user_info_failed",
		Message: "Failed to fetch GitHub user information",
		Code:    http.StatusBadRequest,
	}
)

// NewAuthenticationError creates a new authentication error with a cause based on a base error.
func NewAuthenticationError(baseErr *AuthenticationError, cause error) *AuthenticationError {
	return &AuthenticationError{
		Type:    baseErr.Type,
		Message: baseErr.Message,
		Code:    baseErr.Code,
		Cause:   cause,
	}
}

// IsAuthenticationError checks if an error is an authentication error.
func IsAuthenticationError(err error) bool {
	var authenticationError *AuthenticationError
	ok := errors.As(err, &authenticationError)
	return ok
}

// IsOAuthError checks if an error is an OAuth error.
func IsOAuthError(err error) bool {
	var oAuthError *OAuthError
	ok := errors.As(err, &oAuthError)
	return ok
}

// GetUserFriendlyMessage returns a user-friendly error message based on the error type.
func GetUserFriendlyMessage(err error) string {
	var authErr *AuthenticationError
	if errors.As(err, &authErr) {
		switch authErr.Type {
		case "device_code_failed":
			return "Failed to start GitHub authentication. Please check your network connection and try again."
		case "device_code_expired":
			return "The authentication code has expired. Please try again."
		case "authorization_pending":
			return "Waiting for you to authorize the application on GitHub."
		case "slow_down":
			return "Please wait a moment before trying again."
		case "access_denied":
			return "Authentication was cancelled or denied."
		case "token_exchange_failed":
			return "Failed to complete authentication. Please try again."
		case "polling_timeout":
			return "Authentication timed out. Please try again."
		case "user_info_failed":
			return "Failed to get your GitHub account information. Please try again."
		default:
			return "Authentication failed. Please try again."
		}
	}

	var oauthErr *OAuthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.Code {
		case "access_denied":
			return "Authentication was cancelled or denied."
		case "invalid_request":
			return "Invalid authentication request. Please try again."
		case "server_error":
			return "GitHub server error. Please try again later."
		default:
			return fmt.Sprintf("Authentication failed: %s", oauthErr.Description)
		}
	}

	return "An unexpected error occurred. Please try again."
}
