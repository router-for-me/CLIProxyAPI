package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManagerPruneExpiredAvailability_ClearsExpiredModelQuotaWarning(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Minute)
	model := "gpt-5"
	rawUsageLimit := `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`
	m := NewManager(nil, nil, nil)
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:             "quota-auth",
		Provider:       "codex",
		Status:         StatusError,
		StatusMessage:  rawUsageLimit,
		Unavailable:    true,
		NextRetryAfter: past,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: past,
		},
		LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: rawUsageLimit},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  rawUsageLimit,
				Unavailable:    true,
				NextRetryAfter: past,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: past,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: rawUsageLimit},
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if got := m.PruneExpiredAvailability(context.Background(), now); got != 1 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 1", got)
	}

	updated, ok := m.GetByID("quota-auth")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Status != StatusActive {
		t.Fatalf("auth status = %q, want %q", updated.Status, StatusActive)
	}
	if updated.StatusMessage != "" {
		t.Fatalf("auth status message = %q, want empty", updated.StatusMessage)
	}
	if updated.Unavailable {
		t.Fatalf("auth unavailable = true, want false")
	}
	if updated.LastError != nil {
		t.Fatalf("auth last error = %#v, want nil", updated.LastError)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !modelStateIsClean(state) {
		t.Fatalf("model state = %+v, want clean", state)
	}
}

func TestManagerPruneExpiredAvailability_KeepsActiveModelQuotaWarning(t *testing.T) {
	now := time.Now()
	next := now.Add(time.Hour)
	model := "gpt-5"
	m := NewManager(nil, nil, nil)
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:             "quota-auth-active",
		Provider:       "codex",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: next,
		},
		LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if got := m.PruneExpiredAvailability(context.Background(), now); got != 0 {
		t.Fatalf("PruneExpiredAvailability() = %d, want 0", got)
	}

	updated, ok := m.GetByID("quota-auth-active")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Status != StatusError {
		t.Fatalf("auth status = %q, want %q", updated.Status, StatusError)
	}
	if !updated.Unavailable {
		t.Fatalf("auth unavailable = false, want true")
	}
	if state := updated.ModelStates[model]; state == nil || !state.Unavailable {
		t.Fatalf("model state = %+v, want unavailable", state)
	}
}
