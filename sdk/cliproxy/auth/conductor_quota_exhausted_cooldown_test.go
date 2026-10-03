package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// quotaExhaustedResult builds the result shape an Antigravity executor produces for an
// exhausted quota window: a 429 whose error carries the upstream reset deadline.
func quotaExhaustedResult(authID, model string, resetAfter time.Duration) Result {
	return Result{
		AuthID:     authID,
		Provider:   "antigravity",
		Model:      model,
		RouteModel: model,
		Success:    false,
		Error:      &Error{HTTPStatus: 429, Message: "Individual quota reached"},
		RetryAfter: &resetAfter,
	}
}

// coolingAuth builds an Antigravity credential whose given model sits in a quota
// cooldown until 69h after now, matching the shape an exhausted window produces.
func coolingAuth(now time.Time, id, model string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "antigravity",
		Status:   StatusError,
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: now.Add(69 * time.Hour),
				},
				NextRetryAfter: now.Add(69 * time.Hour),
			},
		},
	}
}

func TestMarkResult_QuotaExhaustedCoolsOnlyTheExhaustedModel(t *testing.T) {
	const model = "gemini-3-pro"
	const otherModel = "claude-sonnet-4-6"
	resetAfter := 69*time.Hour + 2*time.Minute + 37*time.Second

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "antigravity-1", Provider: "antigravity", Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), quotaExhaustedResult("antigravity-1", model, resetAfter))

	stored, ok := manager.GetByID("antigravity-1")
	if !ok || stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	state := stored.ModelStates[model]
	if state == nil {
		t.Fatalf("ModelStates = %+v, want an entry for %q", stored.ModelStates, model)
	}
	if !state.Unavailable || state.Status != StatusError {
		t.Fatalf("state = %+v, want unavailable status error", state)
	}
	if !state.Quota.Exceeded || state.Quota.Reason != "quota" {
		t.Fatalf("state quota = %+v, want exceeded with reason quota", state.Quota)
	}
	wantRecover := before.Add(resetAfter)
	if delta := state.Quota.NextRecoverAt.Sub(wantRecover); delta < -time.Minute || delta > time.Minute {
		t.Fatalf("state quota NextRecoverAt = %v, want about %v", state.Quota.NextRecoverAt, wantRecover)
	}
	if otherState := stored.ModelStates[otherModel]; otherState != nil {
		t.Fatalf("ModelStates[%q] = %+v, want no cooldown for an unaffected model", otherModel, otherState)
	}
	// The credential-level quota aggregates per-model cooldowns with reason "quota".
	// Only "credential_quota" would disable the whole credential, so the sibling model
	// stays selectable.
	if stored.Quota.Reason == "credential_quota" {
		t.Fatalf("auth quota = %+v, want a model-scoped cooldown rather than a credential-wide one", stored.Quota)
	}
}

// TestMarkResult_QuotaExhaustedCoolsBeyondBackoffLadder pins the cooldown to the upstream
// reset time rather than the exponential backoff, which would clear long before a weekly
// quota window reopens.
func TestMarkResult_QuotaExhaustedCoolsBeyondBackoffLadder(t *testing.T) {
	const model = "gemini-3-pro"
	resetAfter := 72 * time.Hour

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "antigravity-backoff", Provider: "antigravity", Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), quotaExhaustedResult("antigravity-backoff", model, resetAfter))

	stored, ok := manager.GetByID("antigravity-backoff")
	if !ok || stored == nil {
		t.Fatal("auth was not found after MarkResult")
	}
	state := stored.ModelStates[model]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("ModelStates[%q] = %+v, want an exceeded quota cooldown", model, state)
	}
	if remaining := state.Quota.NextRecoverAt.Sub(before); remaining < resetAfter-time.Minute {
		t.Fatalf("cooldown remaining = %v, want at least %v", remaining, resetAfter)
	}
	if state.NextRetryAfter.Before(state.Quota.NextRecoverAt) {
		t.Fatalf("state NextRetryAfter = %v, want at least Quota.NextRecoverAt = %v", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}
}

// TestRoundRobinSelectorSkipsModelQuotaCooldown is the regression guard for the reported
// bug: after a model-level quota cooldown the selector must move on to another credential
// for that model instead of re-picking the exhausted one within the same request.
func TestRoundRobinSelectorSkipsModelQuotaCooldown(t *testing.T) {
	const model = "gemini-3-pro"
	cooling := coolingAuth(time.Now(), "antigravity-cooling", model)
	healthy := &Auth{ID: "antigravity-healthy", Provider: "antigravity", Status: StatusActive}
	// The cooling credential served Claude fine, so that model must stay selectable.
	healthy.ModelStates = map[string]*ModelState{
		"claude-sonnet-4-6": {Status: StatusActive},
	}

	selector := &RoundRobinSelector{}
	selected, errPick := selector.Pick(context.Background(), "antigravity", model, cliproxyexecutor.Options{}, []*Auth{cooling, healthy})
	if errPick != nil {
		t.Fatalf("Pick() error = %v, want the healthy credential", errPick)
	}
	if selected.ID != healthy.ID {
		t.Fatalf("Pick() = %q, want %q", selected.ID, healthy.ID)
	}

	selectedClaude, errPickClaude := selector.Pick(context.Background(), "antigravity", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{cooling, healthy})
	if errPickClaude != nil {
		t.Fatalf("Pick() for an unaffected model error = %v, want a credential", errPickClaude)
	}
	if selectedClaude == nil {
		t.Fatal("Pick() for an unaffected model returned no credential")
	}
}

// TestSelectorModelCooldownErrorCarriesRecoveryTime covers the all-cooling path: the
// caller-facing error must state when the quota window reopens.
func TestSelectorModelCooldownErrorCarriesRecoveryTime(t *testing.T) {
	const model = "gemini-3-pro"
	cooling := coolingAuth(time.Now(), "antigravity-cooling", model)

	_, errPick := (&RoundRobinSelector{}).Pick(context.Background(), "antigravity", model, cliproxyexecutor.Options{}, []*Auth{cooling})
	if errPick == nil {
		t.Fatal("Pick() error = nil, want a model cooldown error")
	}
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) || cooldownErr == nil {
		t.Fatalf("Pick() error = %T, want *modelCooldownError", errPick)
	}
	if cooldownErr.resetIn <= 0 {
		t.Fatalf("cooldown error retryAfter = %v, want the upstream recovery time", cooldownErr.resetIn)
	}
}
