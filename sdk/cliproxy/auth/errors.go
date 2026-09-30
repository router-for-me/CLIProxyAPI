package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrorCodeRequestScoped identifies failures tied to the current request rather
// than the selected credential.
const ErrorCodeRequestScoped = "request_scoped"

const requestScopedErrorCode = ErrorCodeRequestScoped

// ErrorCodeConnectionLifecycle marks transport/session lifecycle failures that
// must skip credential cooldown without being treated as request-scoped faults.
const ErrorCodeConnectionLifecycle = "connection_lifecycle"

const connectionLifecycleErrorCode = ErrorCodeConnectionLifecycle

// ErrorCodeTransientTransport marks pre-HTTP dial/TLS/DNS/reset failures that
// must skip credential cooldown and remain eligible for request-retry rounds.
const ErrorCodeTransientTransport = "transient_transport"

const transientTransportErrorCode = ErrorCodeTransientTransport

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

// IsTerminalAuthError checks if err or any error in its chain represents a permanent upstream auth failure.
func IsTerminalAuthError(err error) bool {
	if err == nil {
		return false
	}
	type terminalAuthProvider interface {
		IsTerminalAuth() bool
	}
	var tap terminalAuthProvider
	if errors.As(err, &tap) && tap != nil {
		return tap.IsTerminalAuth()
	}
	return false
}

type terminalAuthError struct {
	*errorWithCause
}

func (e *terminalAuthError) IsTerminalAuth() bool {
	return true
}

// NewTerminalAuthError wraps an *Error and cause as a terminal upstream authentication failure.
func NewTerminalAuthError(err *Error, cause error) error {
	if err == nil {
		return nil
	}
	base := WithCause(err, cause)
	if ewc, ok := base.(*errorWithCause); ok && ewc != nil {
		return &terminalAuthError{errorWithCause: ewc}
	}
	return &terminalAuthError{errorWithCause: &errorWithCause{base: err, cause: cause}}
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

type errorWithCause struct {
	base  *Error
	cause error
}

func (e *errorWithCause) Error() string {
	if e == nil || e.base == nil {
		return ""
	}
	baseText := e.base.Error()
	if e.cause == nil {
		return baseText
	}
	summary := ExtractUpstreamErrorSummary(e.cause.Error())
	if summary != "" && !strings.Contains(baseText, summary) {
		return fmt.Sprintf("%s (last upstream error: %s)", baseText, summary)
	}
	return baseText
}

func (e *errorWithCause) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *errorWithCause) As(target any) bool {
	if e == nil {
		return false
	}
	if t, ok := target.(**Error); ok {
		*t = e.base
		return true
	}
	return false
}

func (e *errorWithCause) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == e || target == e.base {
		return true
	}
	if other, ok := target.(*errorWithCause); ok && other != nil {
		return e.base == other.base
	}
	if otherErr, ok := target.(*Error); ok && otherErr != nil {
		return e.base == otherErr
	}
	return false
}

func (e *errorWithCause) StatusCode() int {
	if e == nil || e.base == nil {
		return 0
	}
	return e.base.HTTPStatus
}

func (e *errorWithCause) IsRequestScoped() bool {
	if e == nil || e.base == nil {
		return false
	}
	return e.base.IsRequestScoped()
}

func (e *errorWithCause) MarkRequestScoped() *Error {
	if e == nil || e.base == nil {
		return nil
	}
	return e.base.MarkRequestScoped()
}

func (e *errorWithCause) MarshalJSON() ([]byte, error) {
	if e == nil || e.base == nil {
		return []byte("null"), nil
	}
	return json.Marshal(e.base)
}

// Keep Error's public four-field layout unchanged for downstream unkeyed literals.
type authUnavailableError struct {
	cause      error
	base       *Error
	retryAfter time.Duration
}

func newAuthUnavailableError(next, now time.Time) error {
	return newAuthUnavailableErrorWithCause(next, now, nil)
}

func newAuthUnavailableErrorWithCause(next, now time.Time, cause error) error {
	err := &Error{Code: "auth_unavailable", Message: "no auth available"}
	if next.After(now) {
		err.HTTPStatus = http.StatusServiceUnavailable
		err.Retryable = true
		return &authUnavailableError{
			cause:      cause,
			base:       err,
			retryAfter: next.Sub(now),
		}
	}
	if cause != nil {
		return WithCause(err, cause)
	}
	return err
}

func (e *authUnavailableError) Error() string {
	if e == nil || e.base == nil {
		return ""
	}
	baseText := e.base.Error()
	if e.cause == nil {
		return baseText
	}
	summary := ExtractUpstreamErrorSummary(e.cause.Error())
	if summary != "" && !strings.Contains(baseText, summary) {
		return fmt.Sprintf("%s (last upstream error: %s)", baseText, summary)
	}
	return baseText
}

func (e *authUnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *authUnavailableError) As(target any) bool {
	if e == nil {
		return false
	}
	if t, ok := target.(**Error); ok {
		*t = e.base
		return true
	}
	return false
}

func (e *authUnavailableError) Is(target error) bool {
	if e == nil {
		return target == nil
	}
	if target == e || target == e.base {
		return true
	}
	if other, ok := target.(*authUnavailableError); ok && other != nil {
		return e.base == other.base
	}
	if otherErr, ok := target.(*Error); ok && otherErr != nil {
		return e.base == otherErr
	}
	return false
}

func (e *authUnavailableError) StatusCode() int {
	if e == nil || e.base == nil {
		return 0
	}
	return e.base.HTTPStatus
}

func (e *authUnavailableError) IsRequestScoped() bool {
	if e == nil || e.base == nil {
		return false
	}
	return e.base.IsRequestScoped()
}

func (e *authUnavailableError) MarkRequestScoped() *Error {
	if e == nil || e.base == nil {
		return nil
	}
	return e.base.MarkRequestScoped()
}

func (e *authUnavailableError) MarshalJSON() ([]byte, error) {
	if e == nil || e.base == nil {
		return []byte("null"), nil
	}
	return json.Marshal(e.base)
}

// WithAuthError retains the trusted deadline when the handler enriches context.
func (e *authUnavailableError) WithAuthError(cause *Error) error {
	if e == nil {
		return cause
	}
	return &authUnavailableError{
		cause:      e.cause,
		base:       cause,
		retryAfter: e.retryAfter,
	}
}

// Headers exposes only the locally computed recovery hint, never upstream headers.
func (e *authUnavailableError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return safeRetryAfterHeader(e.retryAfter)
}

// WithCause wraps an *Error with an underlying cause without changing the Error struct layout.
func WithCause(err *Error, cause error) error {
	if err == nil {
		return nil
	}
	if cause == nil {
		return err
	}
	return &errorWithCause{
		base:  err,
		cause: cause,
	}
}
