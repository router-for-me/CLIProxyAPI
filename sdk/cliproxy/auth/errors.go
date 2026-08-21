package auth

import (
	"errors"
	"strings"
)

// ErrorCodeRequestScoped identifies failures tied to the current request rather
// than the selected credential.
const ErrorCodeRequestScoped = "request_scoped"

const requestScopedErrorCode = ErrorCodeRequestScoped

// ErrorCodeConnectionLifecycle marks transport/session lifecycle failures that
// must skip credential cooldown without being treated as request-scoped faults.
const ErrorCodeConnectionLifecycle = "connection_lifecycle"

const connectionLifecycleErrorCode = ErrorCodeConnectionLifecycle

// ErrorCodeForceCooldown marks failures that must enforce credential cooldown.
const ErrorCodeForceCooldown = "force_cooldown"

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

// IsRequestScoped reports whether the failure is tied to the current request
// rather than the selected credential.
func (e *Error) IsRequestScoped() bool {
	return e != nil && e.Code == ErrorCodeRequestScoped
}

// MarkRequestScoped marks the error as request-scoped in place and returns it.
func (e *Error) MarkRequestScoped() *Error {
	if e != nil {
		e.Code = ErrorCodeRequestScoped
	}
	return e
}

// NewRequestScopedError creates an Error explicitly flagged as request-scoped so
// that credential cooldown is skipped.
func NewRequestScopedError(message string, httpStatus int) *Error {
	return &Error{
		Code:       ErrorCodeRequestScoped,
		Message:    message,
		HTTPStatus: httpStatus,
	}
}

// diagnosticError attaches server-side detail to a selection failure. The detail
// is kept beside the error rather than inside Error so the exported struct stays
// usable as an unkeyed literal, and so the text can never be serialized toward a
// client by accident.
type diagnosticError struct {
	cause      *Error
	diagnostic string
}

// Error implements the error interface.
func (e *diagnosticError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

// Unwrap exposes the underlying Error so errors.As keeps working on it.
func (e *diagnosticError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// withDiagnostic wraps err with server-side detail, returning err unchanged when
// there is nothing to add.
func withDiagnostic(err *Error, diagnostic string) error {
	if err == nil {
		return nil
	}
	if strings.TrimSpace(diagnostic) == "" {
		return err
	}
	return &diagnosticError{cause: err, diagnostic: diagnostic}
}

// SelectionDiagnostic returns the server-side detail recorded for a failed auth
// selection, or an empty string when the error carries none. The detail is meant
// for logs and must not be returned to clients.
func SelectionDiagnostic(err error) string {
	var diagErr *diagnosticError
	if errors.As(err, &diagErr) && diagErr != nil {
		return diagErr.diagnostic
	}
	return ""
}
