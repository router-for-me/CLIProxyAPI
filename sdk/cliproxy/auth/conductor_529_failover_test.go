package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// overloadedError mimics the Claude executor's statusErr for a 529
// overloaded_error: it exposes a status code but no Retry-After.
type overloadedError struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e overloadedError) Error() string              { return e.msg }
func (e overloadedError) StatusCode() int            { return e.code }
func (e overloadedError) RetryAfter() *time.Duration { return e.retryAfter }

// withTransientCooldownDefault pins the transient-error cooldown to the legacy
// default so a sibling test that disabled it cannot leak into these assertions.
func withTransientCooldownDefault(t *testing.T) {
	t.Helper()
	prev := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prev) })
}

func newRetryManager(t *testing.T, provider string) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)
	if _, errRegister := m.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-529",
		Provider: provider,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return m
}

func TestShouldRetryAfterError_OverloadedIsRetryableWithDefaultWait(t *testing.T) {
	m := newRetryManager(t, "claude")
	_, _, maxWait := m.retrySettings()

	errOverloaded := overloadedError{code: statusOverloaded, msg: `{"type":"error","error":{"type":"overloaded_error"}}`}
	wait, shouldRetry := m.shouldRetryAfterError(errOverloaded, 0, []string{"claude"}, "claude-opus-4", maxWait)
	if !shouldRetry {
		t.Fatalf("shouldRetryAfterError(529) = (%v, false), want retry", wait)
	}
	if wait != defaultRetryWaitWithoutRetryAfter {
		t.Fatalf("wait = %v, want %v", wait, defaultRetryWaitWithoutRetryAfter)
	}
}

func TestShouldRetryAfterError_TooManyRequestsWithoutRetryAfterUsesDefaultWait(t *testing.T) {
	m := newRetryManager(t, "claude")
	_, _, maxWait := m.retrySettings()

	// *Error carries a status but never a Retry-After, matching Anthropic's
	// headerless OAuth 429.
	errQuota := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate_limit_error"}
	wait, shouldRetry := m.shouldRetryAfterError(errQuota, 0, []string{"claude"}, "claude-opus-4", maxWait)
	if !shouldRetry {
		t.Fatalf("shouldRetryAfterError(429, no Retry-After) = (%v, false), want retry", wait)
	}
	if wait != defaultRetryWaitWithoutRetryAfter {
		t.Fatalf("wait = %v, want %v", wait, defaultRetryWaitWithoutRetryAfter)
	}
}

func TestShouldRetryAfterError_HonoursRetryAfterWhenPresent(t *testing.T) {
	m := newRetryManager(t, "claude")
	_, _, maxWait := m.retrySettings()

	retryAfter := 5 * time.Second
	errQuota := overloadedError{code: http.StatusTooManyRequests, msg: "rate_limit_error", retryAfter: &retryAfter}
	wait, shouldRetry := m.shouldRetryAfterError(errQuota, 0, []string{"claude"}, "claude-opus-4", maxWait)
	if !shouldRetry {
		t.Fatalf("shouldRetryAfterError(429 with Retry-After) = (%v, false), want retry", wait)
	}
	if wait != retryAfter {
		t.Fatalf("wait = %v, want %v", wait, retryAfter)
	}

	// A Retry-After beyond the ceiling is still refused.
	tooLong := maxWait + time.Second
	errLong := overloadedError{code: http.StatusTooManyRequests, msg: "rate_limit_error", retryAfter: &tooLong}
	if wait, shouldRetry = m.shouldRetryAfterError(errLong, 0, []string{"claude"}, "claude-opus-4", maxWait); shouldRetry {
		t.Fatalf("shouldRetryAfterError(Retry-After > maxWait) = (%v, true), want no retry", wait)
	}
}

func TestMarkResultOverloadedSetsTransientCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	withTransientCooldownDefault(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-overloaded",
		Provider: "claude",
		Metadata: map[string]any{"type": "claude"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	model := "claude-opus-4"
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "overloaded_error",
			Message:    "Overloaded",
			Retryable:  true,
			HTTPStatus: statusOverloaded,
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("expected model state after 529 failure")
	}
	state := updated.ModelStates[model]
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter is zero after 529; want a transient cooldown")
	}
	if !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("NextRetryAfter = %v, want a deadline in the future", state.NextRetryAfter)
	}
	if state.Quota.Exceeded {
		t.Fatalf("529 marked the model quota-exceeded; want transient handling only")
	}
}

func TestApplyAuthFailureStateOverloadedSetsTransientCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	withTransientCooldownDefault(t)

	now := time.Now()
	auth := &Auth{ID: "auth-overloaded-scoped", Provider: "claude"}
	applyAuthFailureState(auth, &Error{
		Code:       "overloaded_error",
		Message:    "Overloaded",
		HTTPStatus: statusOverloaded,
	}, nil, now, false)

	if !auth.NextRetryAfter.After(now) {
		t.Fatalf("NextRetryAfter = %v, want a deadline after %v", auth.NextRetryAfter, now)
	}
	if auth.Quota.Exceeded {
		t.Fatalf("529 marked the auth quota-exceeded; want transient handling only")
	}
	if auth.StatusMessage != "transient upstream error" {
		t.Fatalf("StatusMessage = %q, want %q", auth.StatusMessage, "transient upstream error")
	}
}

func TestStatusCodeFromErrorPreservesOverloaded(t *testing.T) {
	errOverloaded := overloadedError{code: statusOverloaded, msg: "Overloaded"}
	if got := statusCodeFromError(errOverloaded); got != statusOverloaded {
		t.Fatalf("statusCodeFromError() = %d, want %d", got, statusOverloaded)
	}
	if isRequestInvalidError(errOverloaded) {
		t.Fatalf("529 classified as request-invalid; it must stay credential-scoped")
	}
}
