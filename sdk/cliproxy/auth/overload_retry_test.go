package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// overloadManager returns a manager holding one registered anthropic credential
// with request-retry enabled, which is what retryAllowed consults.
func overloadManager(t *testing.T, retry int) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(retry, time.Minute, 0)
	auth := &Auth{
		ID:       "auth-overload",
		Provider: "anthropic",
		Metadata: map[string]any{"type": "anthropic"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	return manager
}

func overloadError(status int) *Error {
	return &Error{
		Code:       "overloaded_error",
		Message:    "Overloaded",
		HTTPStatus: status,
	}
}

// A 529 carries no Retry-After header, so before the transient-overload branch
// existed it fell through the 429 gate and surfaced to the caller unretried.
func TestShouldRetryAfterErrorRetriesTransientOverload(t *testing.T) {
	manager := overloadManager(t, 10)
	providers := []string{"anthropic"}

	for _, status := range []int{529, http.StatusServiceUnavailable} {
		wait, ok := manager.shouldRetryAfterError(overloadError(status), 0, providers, "claude-opus-5", time.Minute)
		if !ok {
			t.Fatalf("status %d: expected retry, got none", status)
		}
		if wait != time.Second {
			t.Fatalf("status %d: expected 1s first backoff, got %v", status, wait)
		}
	}
}

func TestShouldRetryAfterErrorOverloadBackoffDoublesAndCaps(t *testing.T) {
	manager := overloadManager(t, 10)
	providers := []string{"anthropic"}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for attempt, expected := range want {
		wait, ok := manager.shouldRetryAfterError(overloadError(529), attempt, providers, "claude-opus-5", time.Minute)
		if !ok {
			t.Fatalf("attempt %d: expected retry, got none", attempt)
		}
		if wait != expected {
			t.Fatalf("attempt %d: expected %v, got %v", attempt, expected, wait)
		}
	}
}

// The computed backoff must never exceed the configured ceiling.
func TestShouldRetryAfterErrorOverloadClampsToMaxWait(t *testing.T) {
	manager := overloadManager(t, 10)

	wait, ok := manager.shouldRetryAfterError(overloadError(529), 5, []string{"anthropic"}, "claude-opus-5", 500*time.Millisecond)
	if !ok {
		t.Fatalf("expected retry, got none")
	}
	if wait != 500*time.Millisecond {
		t.Fatalf("expected wait clamped to 500ms, got %v", wait)
	}
}

// request-retry still bounds how many overload retries are handed out.
func TestShouldRetryAfterErrorOverloadHonoursRequestRetry(t *testing.T) {
	manager := overloadManager(t, 2)
	providers := []string{"anthropic"}

	if _, ok := manager.shouldRetryAfterError(overloadError(529), 1, providers, "claude-opus-5", time.Minute); !ok {
		t.Fatalf("attempt 1 is within request-retry=2, expected retry")
	}
	if _, ok := manager.shouldRetryAfterError(overloadError(529), 2, providers, "claude-opus-5", time.Minute); ok {
		t.Fatalf("attempt 2 exhausts request-retry=2, expected no retry")
	}
}

// Statuses that are not capacity related must keep falling through untouched.
func TestShouldRetryAfterErrorLeavesNonOverloadStatuses(t *testing.T) {
	manager := overloadManager(t, 10)
	providers := []string{"anthropic"}

	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError, http.StatusBadGateway} {
		if _, ok := manager.shouldRetryAfterError(overloadError(status), 0, providers, "claude-opus-5", time.Minute); ok {
			t.Fatalf("status %d: expected no retry from the overload branch", status)
		}
	}
}

// 429 keeps its Retry-After driven behaviour: no hint means no retry.
func TestShouldRetryAfterErrorTooManyRequestsStillNeedsRetryAfter(t *testing.T) {
	manager := overloadManager(t, 10)

	if _, ok := manager.shouldRetryAfterError(overloadError(http.StatusTooManyRequests), 0, []string{"anthropic"}, "claude-opus-5", time.Minute); ok {
		t.Fatalf("429 without Retry-After should not retry")
	}
}
