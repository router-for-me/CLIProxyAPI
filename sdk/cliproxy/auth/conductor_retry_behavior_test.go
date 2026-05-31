package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestManager_ShouldRetryAfterError_PrefersUpstreamRetryAfterFor429(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)

	model := "test-model"
	next := time.Now().Add(20 * time.Second)
	auth := &Auth{
		ID:       "auth-upstream-retry-after",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
					BackoffLevel:  3,
				},
			},
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, _, maxWait := m.retrySettings()
	wait, shouldRetry := m.shouldRetryAfterError(&retryAfterStatusError{
		status:     http.StatusTooManyRequests,
		message:    "quota exhausted",
		retryAfter: 5 * time.Second,
	}, 0, []string{"claude"}, model, maxWait)
	if !shouldRetry {
		t.Fatalf("expected shouldRetry=true, got false")
	}
	if wait != 5*time.Second {
		t.Fatalf("wait = %v, want %v", wait, 5*time.Second)
	}
}

func TestManager_MarkResult_SuccessPreservesQuotaBackoffLevel(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-preserve-backoff",
		Provider: "openai",
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-backoff"
	retryAfter := 5 * time.Second
	m.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Model:      model,
		Success:    false,
		RetryAfter: &retryAfter,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "quota exhausted",
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to exist after first 429")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state after first 429")
	}
	if state.Quota.BackoffLevel != 1 {
		t.Fatalf("backoff after first 429 = %d, want 1", state.Quota.BackoffLevel)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  true,
	})

	updated, ok = m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to exist after success")
	}
	state = updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state after success")
	}
	if state.Quota.Exceeded {
		t.Fatalf("expected quota exceeded to be cleared on success")
	}
	if state.Quota.BackoffLevel != 1 {
		t.Fatalf("backoff after success = %d, want 1", state.Quota.BackoffLevel)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be cleared on success, got %v", state.NextRetryAfter)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "quota exhausted again",
		},
	})

	updated, ok = m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to exist after second 429")
	}
	state = updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state after second 429")
	}
	if state.Quota.BackoffLevel != 2 {
		t.Fatalf("backoff after second 429 = %d, want 2", state.Quota.BackoffLevel)
	}
}
