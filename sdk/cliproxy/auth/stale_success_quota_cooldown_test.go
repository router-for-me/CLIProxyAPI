package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func registerStaleSuccessTestAuth(t *testing.T, model string) (*Manager, *Auth) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       uuid.NewString() + "-stale-success",
		Provider: "antigravity",
		Status:   StatusActive,
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return manager, auth
}

func markQuotaExhausted(manager *Manager, auth *Auth, model string, startedAt time.Time, cooldown time.Duration) {
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "antigravity",
		Model:      model,
		Success:    false,
		RetryAfter: &cooldown,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
		StartedAt:  startedAt,
	})
}

func TestMarkResult_SuccessDispatchedBeforeQuotaCooldownKeepsCooldown(t *testing.T) {
	const model = "gemini-stale-success-race"
	manager, auth := registerStaleSuccessTestAuth(t, model)

	// Request A is dispatched first and is still in flight when request B,
	// dispatched slightly later on the same credential, reports quota exhaustion.
	dispatchedA := time.Now()
	dispatchedB := dispatchedA.Add(time.Millisecond)
	markQuotaExhausted(manager, auth, model, dispatchedB, 2*time.Hour)

	// Request A then completes successfully.
	manager.MarkResult(context.Background(), Result{
		AuthID:    auth.ID,
		Provider:  "antigravity",
		Model:     model,
		Success:   true,
		StartedAt: dispatchedA,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	blocked, reason, next := isAuthBlockedForModel(updated, model, time.Now())
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("model unblocked by a success dispatched before the quota error: blocked=%v reason=%v state=%+v", blocked, reason, updated.ModelStates[model])
	}
	if !next.After(time.Now().Add(time.Hour)) {
		t.Fatalf("quota cooldown shortened by stale success: next=%v", next)
	}
}

func TestMarkResult_SuccessDispatchedAfterQuotaCooldownClearsCooldown(t *testing.T) {
	const model = "gemini-fresh-success-after-quota"
	manager, auth := registerStaleSuccessTestAuth(t, model)

	markQuotaExhausted(manager, auth, model, time.Now(), 2*time.Hour)

	// A request dispatched after the cooldown was recorded (for example a
	// management probe or a manual retry) succeeds: the quota has recovered.
	manager.MarkResult(context.Background(), Result{
		AuthID:    auth.ID,
		Provider:  "antigravity",
		Model:     model,
		Success:   true,
		StartedAt: time.Now().Add(time.Millisecond),
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	if blocked, reason, _ := isAuthBlockedForModel(updated, model, time.Now()); blocked {
		t.Fatalf("fresh success did not clear the quota cooldown: reason=%v state=%+v", reason, updated.ModelStates[model])
	}
}

func TestMarkResult_SuccessWithoutDispatchTimeClearsCooldown(t *testing.T) {
	const model = "gemini-success-without-start"
	manager, auth := registerStaleSuccessTestAuth(t, model)

	markQuotaExhausted(manager, auth, model, time.Now(), 2*time.Hour)

	// Callers that do not report a dispatch time keep the previous behavior.
	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    model,
		Success:  true,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	if blocked, reason, _ := isAuthBlockedForModel(updated, model, time.Now()); blocked {
		t.Fatalf("success without dispatch time did not clear the cooldown: reason=%v", reason)
	}
}

func TestSuccessPredatesQuotaCooldown(t *testing.T) {
	now := time.Now()
	recorded := now.Add(-time.Second)
	active := &ModelState{
		UpdatedAt: recorded,
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)},
	}
	expired := &ModelState{
		UpdatedAt: recorded,
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Minute)},
	}
	notExceeded := &ModelState{UpdatedAt: recorded}

	tests := []struct {
		name      string
		state     *ModelState
		startedAt time.Time
		want      bool
	}{
		{name: "dispatched before active cooldown", state: active, startedAt: recorded.Add(-time.Millisecond), want: true},
		{name: "dispatched after active cooldown", state: active, startedAt: recorded.Add(time.Millisecond), want: false},
		{name: "dispatched at the same instant", state: active, startedAt: recorded, want: false},
		{name: "cooldown already expired", state: expired, startedAt: recorded.Add(-time.Millisecond), want: false},
		{name: "no quota cooldown", state: notExceeded, startedAt: recorded.Add(-time.Millisecond), want: false},
		{name: "unknown dispatch time", state: active, startedAt: time.Time{}, want: false},
		{name: "no model state", state: nil, startedAt: recorded.Add(-time.Millisecond), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := successPredatesQuotaCooldown(test.state, test.startedAt, now); got != test.want {
				t.Fatalf("successPredatesQuotaCooldown() = %v, want %v", got, test.want)
			}
		})
	}
}
