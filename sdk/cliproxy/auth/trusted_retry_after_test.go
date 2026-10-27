package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestTrustedRetryAfterErrorIdentityAndHeaders(t *testing.T) {
	cause := &Error{Code: "quota", Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	hint := 256 * time.Second
	err := withTrustedRetryAfter(cause, &hint)

	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusCodeFromError(err))
	}
	var base *Error
	if !errors.As(err, &base) || base != cause {
		t.Fatalf("wrapped error must resolve to the original *Error")
	}
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "256" {
		t.Fatalf("trusted Retry-After = %q, want 256", got)
	}
	if got := SafeResponseHeaders(fmt.Errorf("outer: %w", err)).Get("Retry-After"); got != "256" {
		t.Fatalf("trusted Retry-After must survive wrapping, got %q", got)
	}
	got := retryAfterFromError(err)
	if got == nil || *got > hint || *got < hint-time.Second {
		t.Fatalf("retryAfterFromError = %v, want remaining near trusted hint %v", got, hint)
	}
}

type headerCarryingError struct{ headers http.Header }

func (e headerCarryingError) Error() string        { return "upstream" }
func (e headerCarryingError) StatusCode() int      { return http.StatusTooManyRequests }
func (e headerCarryingError) Headers() http.Header { return e.headers.Clone() }

func TestTrustedRetryAfterPreservesUpstreamHeaders(t *testing.T) {
	cause := headerCarryingError{headers: http.Header{"X-Request-Id": []string{"req-1"}, "Retry-After": []string{"30"}}}
	hint := 256 * time.Second
	err := withTrustedRetryAfter(cause, &hint)
	carrier, ok := err.(interface{ Headers() http.Header })
	if !ok || carrier.Headers() == nil {
		t.Fatalf("trusted wrapper must preserve Headers(), err=%T", err)
	}
	if got := carrier.Headers().Get("X-Request-Id"); got != "req-1" {
		t.Fatalf("X-Request-Id = %q, want req-1", got)
	}
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "256" {
		t.Fatalf("trusted Retry-After = %q, want 256", got)
	}
}

func TestHasTrustedRetryDeadlineSurvivesExpiry(t *testing.T) {
	cause := &Error{Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	hint := time.Minute
	err := withTrustedRetryAfter(cause, &hint)
	if !HasTrustedRetryDeadline(err) {
		t.Fatal("live trusted deadline must be reported")
	}
	wrapped := err.(*trustedRetryAfterError)
	wrapped.deadline = time.Now().Add(-time.Second)
	if !HasTrustedRetryDeadline(err) {
		t.Fatal("expired trusted deadline must still be reported so stale raw headers stay suppressed")
	}
	if HasTrustedRetryDeadline(cause) {
		t.Fatal("unannotated errors must not report a trusted deadline")
	}
}

func TestTrustedRetryAfterNonPositiveIsNoop(t *testing.T) {
	cause := &Error{Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	zero := time.Duration(0)
	if err := withTrustedRetryAfter(cause, &zero); err != error(cause) {
		t.Fatalf("zero hint must not wrap, got %T", err)
	}
	if err := withTrustedRetryAfter(cause, nil); err != error(cause) {
		t.Fatalf("nil hint must not wrap, got %T", err)
	}
}

func TestHomeAggregatedDeadlineOutranksTrustedAnnotation(t *testing.T) {
	cause := &Error{Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	perAttempt := 5 * time.Second
	annotated := withTrustedRetryAfter(cause, &perAttempt)
	aggregated := 1500 * time.Millisecond
	terminal := markHomeRetryRoundExhausted(annotated, &aggregated, false)
	if got := SafeResponseHeaders(terminal).Get("Retry-After"); got != "2" {
		t.Fatalf("aggregated deadline must win, got %q want 2", got)
	}
}
