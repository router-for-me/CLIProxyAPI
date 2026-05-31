package grok

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
	return errors.As(err, &authenticationError)
}

// Common authentication error sentinels.
var (
	// ErrPortInUse is returned when port 56121 is already bound. The CLI
	// should catch this and fall back to the device-code flow.
	ErrPortInUse = &AuthenticationError{
		Type:    "port_in_use",
		Message: "OAuth callback port 56121 is already in use",
		Code:    13, // Special exit code for port-in-use (matches codex convention)
	}

	// ErrCallbackTimeout is returned when the browser has not posted back
	// within the 5-minute window.
	ErrCallbackTimeout = &AuthenticationError{
		Type:    "callback_timeout",
		Message: "Timeout waiting for OAuth callback",
		Code:    http.StatusRequestTimeout,
	}

	// ErrInvalidState is returned when the state param in the callback does
	// not match the one supplied to GenerateAuthURL (CSRF guard).
	ErrInvalidState = &AuthenticationError{
		Type:    "invalid_state",
		Message: "OAuth state parameter is invalid or missing",
		Code:    http.StatusBadRequest,
	}

	// ErrCodeExchangeFailed is returned when the token endpoint returns a
	// non-2xx response during code exchange.
	ErrCodeExchangeFailed = &AuthenticationError{
		Type:    "code_exchange_failed",
		Message: "Failed to exchange authorization code for tokens",
		Code:    http.StatusBadRequest,
	}
)
