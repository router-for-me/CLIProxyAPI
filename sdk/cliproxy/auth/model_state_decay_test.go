package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// TestModelStateDecayOnSuccess covers the G2 staged recovery: a success halves
// the per-model failure count and keeps a short residual cooldown instead of
// instantly restoring full health. Repeated successes decay to the historical
// full reset.
func TestModelStateDecayOnSuccess(t *testing.T) {
	withQuotaCooldownEnabled(t)
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0) // legacy default: 1-minute transient cooldown
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	const (
		provider = "openai-compat-decay-test"
		model    = "decay-test-model"
		authID   = "decay-test-auth"
	)
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}

	fail := func() {
		t.Helper()
		manager.MarkResult(context.Background(), Result{
			AuthID:   authID,
			Provider: provider,
			Model:    model,
			Success:  false,
			Error:    &Error{Message: "upstream failure", HTTPStatus: http.StatusServiceUnavailable},
		})
	}
	succeed := func() {
		t.Helper()
		manager.MarkResult(context.Background(), Result{
			AuthID:   authID,
			Provider: provider,
			Model:    model,
			Success:  true,
		})
	}
	state := func() *ModelState {
		t.Helper()
		updated, ok := manager.GetByID(authID)
		if !ok || updated == nil {
			t.Fatalf("auth %q missing", authID)
		}
		return updated.ModelStates[model]
	}

	for i := 0; i < 4; i++ {
		fail()
	}
	if got := state().FailureCount; got != 4 {
		t.Fatalf("FailureCount after 4 failures = %d, want 4", got)
	}
	if blocked, _, _ := isAuthBlockedForModel(manager.auths[authID], model, time.Now()); !blocked {
		t.Fatal("auth not blocked for model after 4 failures")
	}

	// First success: 4 -> 2, with a residual cooldown of roughly half the
	// remaining 1-minute window (~30s). Generous bounds avoid flakes.
	successAt := time.Now()
	succeed()
	decayed := state()
	if got := decayed.FailureCount; got != 2 {
		t.Fatalf("FailureCount after first success = %d, want 2", got)
	}
	if !decayed.Unavailable {
		t.Fatal("decayed state Unavailable = false, want true (residual cooldown)")
	}
	if !decayed.NextRetryAfter.After(successAt.Add(20*time.Second)) || !decayed.NextRetryAfter.Before(successAt.Add(40*time.Second)) {
		t.Fatalf("residual NextRetryAfter = %v, want ~%v", decayed.NextRetryAfter, successAt.Add(30*time.Second))
	}
	if blocked, _, _ := isAuthBlockedForModel(manager.auths[authID], model, time.Now()); !blocked {
		t.Fatal("auth still blocked immediately after decayed success")
	}
	if blocked, _, _ := isAuthBlockedForModel(manager.auths[authID], model, successAt.Add(25*time.Second)); !blocked {
		t.Fatal("auth unblocked before residual cooldown (~30s) elapsed")
	}
	if blocked, _, _ := isAuthBlockedForModel(manager.auths[authID], model, successAt.Add(35*time.Second)); blocked {
		t.Fatal("auth still blocked after residual cooldown (~30s) elapsed")
	}

	// Second success: 2 -> 1, residual cooldown halves again (~15s).
	successAt = time.Now()
	succeed()
	decayed = state()
	if got := decayed.FailureCount; got != 1 {
		t.Fatalf("FailureCount after second success = %d, want 1", got)
	}
	if !decayed.NextRetryAfter.After(successAt.Add(8*time.Second)) || !decayed.NextRetryAfter.Before(successAt.Add(22*time.Second)) {
		t.Fatalf("second residual NextRetryAfter = %v, want ~%v", decayed.NextRetryAfter, successAt.Add(15*time.Second))
	}

	// Third success: FailureCount <= 1 takes the historical full reset.
	succeed()
	reset := state()
	if got := reset.FailureCount; got != 0 {
		t.Fatalf("FailureCount after final success = %d, want 0", got)
	}
	if !modelStateIsClean(reset) {
		t.Fatalf("model state not clean after final success: %+v", reset)
	}
	if blocked, _, _ := isAuthBlockedForModel(manager.auths[authID], model, time.Now()); blocked {
		t.Fatal("auth blocked after full-reset success")
	}
}

// TestDecayModelStateOnSuccess covers the pure helper edge cases.
func TestDecayModelStateOnSuccess(t *testing.T) {
	now := time.Now()

	t.Run("nil state does not panic", func(t *testing.T) {
		decayModelStateOnSuccess(nil, now)
	})

	t.Run("count 1 takes full reset", func(t *testing.T) {
		state := &ModelState{
			Status:         StatusError,
			Unavailable:    true,
			StatusMessage:  "transient upstream error",
			NextRetryAfter: now.Add(time.Minute),
			LastError:      &Error{Message: "boom", HTTPStatus: http.StatusServiceUnavailable},
			FailureCount:   1,
			UpdatedAt:      now.Add(-time.Second),
		}
		decayModelStateOnSuccess(state, now)
		if !modelStateIsClean(state) {
			t.Fatalf("expected full reset, got %+v", state)
		}
		if !state.NextRetryAfter.IsZero() {
			t.Fatalf("NextRetryAfter = %v, want zero after full reset", state.NextRetryAfter)
		}
	})

	t.Run("count 0 with deadlines takes full reset", func(t *testing.T) {
		state := &ModelState{
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Minute), BackoffLevel: 2},
			FailureCount:   0,
			UpdatedAt:      now.Add(-time.Second),
		}
		decayModelStateOnSuccess(state, now)
		if !modelStateIsClean(state) {
			t.Fatalf("expected full reset, got %+v", state)
		}
		if !state.Quota.NextRecoverAt.IsZero() {
			t.Fatalf("Quota.NextRecoverAt = %v, want zero after full reset", state.Quota.NextRecoverAt)
		}
	})

	t.Run("count 4 halves with residual cooldown", func(t *testing.T) {
		state := &ModelState{
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
			FailureCount:   4,
			UpdatedAt:      now.Add(-time.Second),
		}
		decayModelStateOnSuccess(state, now)
		if state.FailureCount != 2 {
			t.Fatalf("FailureCount = %d, want 2", state.FailureCount)
		}
		if !state.Unavailable {
			t.Fatal("Unavailable = false, want true (residual cooldown)")
		}
		if !state.NextRetryAfter.After(now.Add(20*time.Second)) || !state.NextRetryAfter.Before(now.Add(40*time.Second)) {
			t.Fatalf("NextRetryAfter = %v, want ~%v", state.NextRetryAfter, now.Add(30*time.Second))
		}
		if state.Quota.NextRecoverAt.IsZero() {
			t.Fatal("Quota.NextRecoverAt zero, want 1s floor for missing deadline")
		}
	})
}

// TestModelStateDecayCloneAndMerge covers the FailureCount mutation surface
// outside MarkResult: Clone copies it and mergeModelState takes the max.
func TestModelStateDecayCloneAndMerge(t *testing.T) {
	now := time.Now()
	state := &ModelState{Status: StatusError, Unavailable: true, FailureCount: 3, UpdatedAt: now}
	cloned := state.Clone()
	if cloned.FailureCount != 3 {
		t.Fatalf("Clone() FailureCount = %d, want 3", cloned.FailureCount)
	}

	merged := mergeModelState(state.Clone(), &ModelState{Status: StatusError, FailureCount: 5, UpdatedAt: now})
	if merged.FailureCount != 5 {
		t.Fatalf("mergeModelState FailureCount = %d, want max(3, 5) = 5", merged.FailureCount)
	}

	merged = mergeModelState(&ModelState{Status: StatusError, FailureCount: 7, UpdatedAt: now}, &ModelState{Status: StatusError, FailureCount: 2, UpdatedAt: now})
	if merged.FailureCount != 7 {
		t.Fatalf("mergeModelState FailureCount = %d, want max(7, 2) = 7", merged.FailureCount)
	}
}
