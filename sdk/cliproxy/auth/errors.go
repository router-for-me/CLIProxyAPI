package auth

import "fmt"

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
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// authExhaustedError wraps a last upstream error into a 503 "no available account"
// message so callers see a clear reason instead of a raw upstream error body.
func authExhaustedError(lastErr error) *Error {
	msg := "no available account"
	if lastErr != nil {
		msg = fmt.Sprintf("no available account: %s", lastErr)
	}
	return &Error{Code: "auth_exhausted", Message: msg, HTTPStatus: 503}
}
