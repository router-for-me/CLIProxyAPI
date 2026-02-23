package auth

import "net/http"

// Error describes an authentication related failure in a provider agnostic format.
type Error struct {
	// Code is a short machine readable identifier.
	Code string `json:"code,omitempty"`
	// Message is a human readable description of the failure.
	Message string `json:"message"`
	// Retryable indicates whether a retry might fix the issue automatically.
	Retryable bool `json:"retryable"`
	// HTTPStatus optionally records an HTTP-like status code for the error.
	HTTPStatus int `json:"http_status,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode implements optional status accessor for manager decision making.
// Returns HTTP 503 Service Unavailable for credential availability issues to
// distinguish from internal server errors (500).
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	if e.HTTPStatus > 0 {
		return e.HTTPStatus
	}
	// Default to 503 for auth availability issues to distinguish from 500 internal errors.
	// This allows clients to implement proper fallback/retry logic.
	switch e.Code {
	case "auth_not_found", "provider_not_found", "executor_not_found":
		return http.StatusServiceUnavailable
	default:
		return 0
	}
}
