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

// ErrorCodeStreamTruncated marks locally synthesized failures for accepted
// upstream streams that ended without a terminal frame. The public status
// stays 502, but the credential must not cool and the generation must not be
// replayed on another credential.
const ErrorCodeStreamTruncated = "stream_truncated"

const streamTruncatedErrorCode = ErrorCodeStreamTruncated

// ErrorCodeTransientRateLimit marks a transient per-model capacity or
// admission 429 (for example a Codex turn_rate_limit_exceeded rejection).
// The public status stays 429, but the credential quota is not exhausted:
// only the reporting model cools until the provider retry hint, without
// quota labelling or quota exponential backoff.
const ErrorCodeTransientRateLimit = "transient_rate_limit"

const transientRateLimitErrorCode = ErrorCodeTransientRateLimit

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

// trustedRetryAfterError annotates a terminal upstream rejection whose
// locally recorded cooldown deadline governs the client-visible Retry-After.
// The wrapped error keeps its own status, body, identity and retry hint; the
// hint stored here is the effective deadline computed when the result was
// recorded, so conflicting attempt headers never reach the client directly.
type trustedRetryAfterError struct {
	cause error
	// deadline is the recovery instant captured when the result was
	// recorded, so a terminal error revisited after waiting reports only
	// the remaining time and never re-advertises the full duration.
	deadline time.Time
}

func trustedRetryAfterHint(err error) *time.Duration {
	if trusted := trustedRetryAfterFromError(err); trusted != nil {
		return trusted
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	value := *retryAfter
	return &value
}

func withTrustedRetryAfter(err error, retryAfter *time.Duration) error {
	if err == nil || retryAfter == nil || *retryAfter <= 0 {
		return err
	}
	if _, ok := err.(*upstreamExecutionAttemptError); ok {
		return markUpstreamExecutionAttempt(withTrustedRetryAfter(unwrapUpstreamExecutionAttempt(err), retryAfter))
	}
	if bootstrap, ok := err.(*streamBootstrapError); ok && bootstrap != nil {
		wrapped := withTrustedRetryAfter(bootstrap.cause, retryAfter)
		if wrapped == bootstrap.cause {
			return err
		}
		// Keep the same wrapper object so callers that already captured this
		// error (for example as the preferred upstream attempt) observe the
		// annotation instead of a stale pre-recording snapshot.
		copyBootstrap := *bootstrap
		copyBootstrap.cause = wrapped
		*bootstrap = copyBootstrap
		return bootstrap
	}
	if requestStop, ok := err.(requestStopError); ok {
		wrapped := withTrustedRetryAfter(requestStop.error, retryAfter)
		if wrapped == requestStop.error {
			return err
		}
		return requestStopError{error: wrapped}
	}
	var existing *trustedRetryAfterError
	if errors.As(err, &existing) && existing != nil {
		return err
	}
	return &trustedRetryAfterError{cause: err, deadline: time.Now().Add(*retryAfter)}
}

// annotateTrustedRetryAfterByHint trusts a positive hint extracted from the
// same error. Executor hints already carry the effective classification
// precedence (quota body reset, connection-limit zero, then Retry-After
// header); a later concurrent cooldown may only have extended the deadline,
// so a trusted annotation may under-report but never over-report recovery.
func annotateTrustedRetryAfterByHint(err error) error {
	if err == nil {
		return err
	}
	return withTrustedRetryAfter(err, trustedRetryAfterHint(err))
}

func (e *trustedRetryAfterError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *trustedRetryAfterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *trustedRetryAfterError) As(target any) bool {
	if e == nil {
		return false
	}
	if t, ok := target.(**Error); ok {
		var base *Error
		if errors.As(e.cause, &base) && base != nil {
			*t = base
			return true
		}
	}
	return false
}

func (e *trustedRetryAfterError) StatusCode() int {
	if e == nil {
		return 0
	}
	return statusCodeFromError(e.cause)
}

func (e *trustedRetryAfterError) RetryAfter() *time.Duration {
	if e == nil || e.deadline.IsZero() {
		return nil
	}
	remaining := time.Until(e.deadline)
	return &remaining
}

// HasTrustedRetryDeadline reports whether err carries a locally trusted
// recovery deadline, even after that deadline has passed. Header writers
// use it to keep a stale raw upstream Retry-After from resurfacing.
func HasTrustedRetryDeadline(err error) bool {
	if err == nil {
		return false
	}
	// An exhausted Home round owns the aggregate decision even when its
	// RetryAfter is nil (immediate / no hint), so raw passthrough stays
	// suppressed for the whole round.
	var exhausted *homeRetryRoundExhaustedError
	if errors.As(err, &exhausted) && exhausted != nil {
		return true
	}
	var unavailable *authUnavailableError
	if errors.As(err, &unavailable) && unavailable != nil && unavailable.retryAfter > 0 {
		return true
	}
	var cooldown *modelCooldownError
	if errors.As(err, &cooldown) && cooldown != nil && cooldown.resetIn > 0 {
		return true
	}
	// Every remaining safe-header carrier (Home busy, Home dispatch
	// cooldown, plugin-provided safe headers) outranks a raw passthrough
	// Retry-After as well.
	if SafeResponseHeaders(err).Get("Retry-After") != "" {
		return true
	}
	var trusted *trustedRetryAfterError
	return errors.As(err, &trusted) && trusted != nil && !trusted.deadline.IsZero()
}

// trustedRetryAfterFromError reads the locally trusted deadline annotation
// only while it is still live. An expired annotation yields nil so callers
// fall back to the underlying provider hint instead of a stale deadline.
func trustedRetryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	var trusted *trustedRetryAfterError
	if errors.As(err, &trusted) && trusted != nil {
		if remaining := trusted.RetryAfter(); remaining != nil && *remaining > 0 {
			return remaining
		}
	}
	return nil
}

// Headers preserves any upstream response headers the wrapped error
// carries (for example a Codex websocket failure) so header passthrough
// keeps working; the trusted deadline still outranks them on write.
func (e *trustedRetryAfterError) Headers() http.Header {
	if e == nil || e.cause == nil {
		return nil
	}
	var carrier interface{ Headers() http.Header }
	if errors.As(e.cause, &carrier) && carrier != nil {
		return carrier.Headers()
	}
	return nil
}

// SafeResponseHeaders exposes only the locally trusted cooldown deadline.
func (e *trustedRetryAfterError) SafeResponseHeaders() http.Header {
	if e == nil || e.deadline.IsZero() {
		return nil
	}
	return safeRetryAfterHeader(time.Until(e.deadline))
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
