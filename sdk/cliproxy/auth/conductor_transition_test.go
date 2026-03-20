package auth

import (
	"testing"
	"time"
)

func TestCaptureAuthSchedulingState_IgnoresNonSchedulingFields(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	base := &Auth{
		ID:             "auth-a",
		Provider:       "gemini",
		Status:         StatusActive,
		StatusMessage:  "old",
		Unavailable:    true,
		NextRetryAfter: now.Add(5 * time.Minute),
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(5 * time.Minute),
			BackoffLevel:  3,
		},
		UpdatedAt: now,
		LastError: &Error{HTTPStatus: 429, Message: "old message"},
		ModelStates: map[string]*ModelState{
			"test-model": {
				Status:         StatusError,
				StatusMessage:  "old model message",
				Unavailable:    true,
				NextRetryAfter: now.Add(5 * time.Minute),
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(5 * time.Minute),
					BackoffLevel:  2,
				},
				UpdatedAt: now,
				LastError: &Error{HTTPStatus: 429, Message: "old model error"},
			},
		},
	}
	mutated := base.Clone()
	mutated.StatusMessage = "new"
	mutated.UpdatedAt = now.Add(10 * time.Minute)
	mutated.LastError = &Error{HTTPStatus: 429, Message: "new auth error"}
	mutated.ModelStates["test-model"].StatusMessage = "new model message"
	mutated.ModelStates["test-model"].UpdatedAt = now.Add(10 * time.Minute)
	mutated.ModelStates["test-model"].LastError = &Error{HTTPStatus: 429, Message: "new model error"}

	before := captureAuthSchedulingState(base, "test-model")
	after := captureAuthSchedulingState(mutated, "test-model")
	if !before.equal(after) {
		t.Fatalf("captureAuthSchedulingState() changed for non-scheduling fields: before=%+v after=%+v", before, after)
	}
}

func TestBuildModelRegistryTransition_ReasonChangeResumesAndSuspends(t *testing.T) {
	t.Parallel()

	transition := buildModelRegistryTransition(
		modelRegistryState{quotaExceeded: true, suspended: true, suspendReason: "quota"},
		modelRegistryState{quotaExceeded: false, suspended: true, suspendReason: "unauthorized"},
	)
	if !transition.clearQuota {
		t.Fatalf("clearQuota = false, want true")
	}
	if transition.setQuota {
		t.Fatalf("setQuota = true, want false")
	}
	if !transition.resumeModel {
		t.Fatalf("resumeModel = false, want true")
	}
	if !transition.suspendModel {
		t.Fatalf("suspendModel = false, want true")
	}
	if transition.suspendReason != "unauthorized" {
		t.Fatalf("suspendReason = %q, want %q", transition.suspendReason, "unauthorized")
	}
}
