package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type requestScopedCooldownError struct {
	status int
	body   string
}

func (e requestScopedCooldownError) Error() string   { return e.body }
func (e requestScopedCooldownError) StatusCode() int { return e.status }

func TestRequestScopedCooldown_SelectsOtherCredentialUntilExpiry(t *testing.T) {
	previousCooling := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooling) })

	manager := NewManager(nil, nil, nil)
	primary := &Auth{
		ID:       "request-scoped-cooldown-primary",
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": "10",
		},
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
				Status:   400,
				Match:    []string{"credit exhausted"},
				Action:   RequestScopedActionContinueAndCooldown,
				Cooldown: "1h",
			}},
		},
	}
	backup := &Auth{
		ID:       "request-scoped-cooldown-backup",
		Provider: "claude",
		Status:   StatusActive,
	}
	model := "claude-3"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(primary.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(backup.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(primary.ID)
		reg.UnregisterClient(backup.ID)
	})
	if _, errRegister := manager.Register(context.Background(), primary); errRegister != nil {
		t.Fatalf("register primary: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), backup); errRegister != nil {
		t.Fatalf("register backup: %v", errRegister)
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "claude"})

	selected, errSelect := manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != primary.ID {
		t.Fatalf("initial SelectAuth() = %v, %v; want primary", selected, errSelect)
	}

	matchedErr := requestScopedCooldownError{status: 400, body: "credit exhausted"}
	action, cooldown, okAction := matchRequestScopedErrorAction(primary, matchedErr, nil)
	if !okAction || action != RequestScopedActionContinueAndCooldown || cooldown != time.Hour {
		t.Fatalf("matchRequestScopedErrorAction() = %q, %v, %v; want continue-and-cooldown, 1h, true", action, cooldown, okAction)
	}
	result := Result{
		AuthID:   primary.ID,
		Provider: "claude",
		Model:    model,
		Error:    resultErrorFromError(matchedErr),
	}
	applyRequestScopedActionToResult(action, cooldown, okAction, &result)
	manager.MarkResult(context.Background(), result)

	manager.mu.RLock()
	primaryState := manager.auths[primary.ID].ModelStates[model]
	primaryNext := primaryState.NextRetryAfter
	manager.mu.RUnlock()
	now := time.Now()
	if !primaryState.Unavailable || primaryNext.Before(now.Add(59*time.Minute)) || primaryNext.After(now.Add(61*time.Minute)) {
		t.Fatalf("primary cooldown = unavailable %v, next %v; want about 1h", primaryState.Unavailable, primaryNext)
	}
	manager.MarkResult(context.Background(), Result{
		AuthID:   primary.ID,
		Provider: "claude",
		Model:    model,
		Error:    &Error{HTTPStatus: 500, Message: "later transient failure"},
	})
	manager.mu.RLock()
	primaryNextAfterLaterFailure := manager.auths[primary.ID].ModelStates[model].NextRetryAfter
	manager.mu.RUnlock()
	if primaryNextAfterLaterFailure.Before(now.Add(59 * time.Minute)) {
		t.Fatalf("later failure shortened primary cooldown to %v", primaryNextAfterLaterFailure)
	}

	selected, errSelect = manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != backup.ID {
		t.Fatalf("SelectAuth() during cooldown = %v, %v; want backup", selected, errSelect)
	}

	manager.mu.Lock()
	primaryAuth := manager.auths[primary.ID]
	primaryAuth.ModelStates[model].NextRetryAfter = time.Now().Add(-time.Second)
	updateAggregatedAvailability(primaryAuth, time.Now())
	primarySnapshot := primaryAuth.Clone()
	backupSnapshot := manager.auths[backup.ID].Clone()
	manager.mu.Unlock()
	manager.scheduler.rebuild([]*Auth{primarySnapshot, backupSnapshot})

	selected, errSelect = manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != primary.ID {
		t.Fatalf("SelectAuth() after cooldown expiry = %v, %v; want primary", selected, errSelect)
	}
}

func TestRequestScopedCooldown_AbsentPreservesTransientBehavior(t *testing.T) {
	previousCooling := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooling) })
	previousTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "request-scoped-cooldown-legacy",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
				Status: 400,
				Match:  []string{"legacy cooldown"},
				Action: RequestScopedActionStopAndCooldown,
			}},
		},
	}
	model := "claude-3"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	matchedErr := requestScopedCooldownError{status: 400, body: "legacy cooldown"}
	action, cooldown, okAction := matchRequestScopedErrorAction(auth, matchedErr, nil)
	if !okAction || action != RequestScopedActionStopAndCooldown || cooldown != 0 {
		t.Fatalf("legacy match = %q, %v, %v; want action, zero cooldown, true", action, cooldown, okAction)
	}
	retryAfter := time.Hour
	result := Result{AuthID: auth.ID, Provider: "claude", Model: model, RetryAfter: &retryAfter, Error: resultErrorFromError(matchedErr)}
	applyRequestScopedActionToResult(action, cooldown, okAction, &result)
	if result.RetryAfter == nil || *result.RetryAfter != retryAfter {
		t.Fatalf("legacy result RetryAfter = %v; want provider hint %v", result.RetryAfter, retryAfter)
	}
	manager.MarkResult(context.Background(), result)

	manager.mu.RLock()
	next := manager.auths[auth.ID].ModelStates[model].NextRetryAfter
	manager.mu.RUnlock()
	if next.Before(time.Now().Add(4*time.Second)) || next.After(time.Now().Add(6*time.Second)) {
		t.Fatalf("legacy cooldown deadline = %v; want about 5s", next)
	}
}

func TestRequestScopedCooldown_InvalidValueIgnoresRule(t *testing.T) {
	auth := &Auth{Metadata: map[string]any{
		"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
			Status:   400,
			Match:    []string{"invalid duration"},
			Action:   RequestScopedActionStopAndCooldown,
			Cooldown: "not-a-duration",
		}},
	}}
	if action, cooldown, okAction := matchRequestScopedErrorAction(auth, requestScopedCooldownError{status: 400, body: "invalid duration"}, nil); okAction || action != "" || cooldown != 0 {
		t.Fatalf("invalid cooldown match = %q, %v, %v; want no match", action, cooldown, okAction)
	}
}

func TestRequestScopedCooldown_ExplicitDurationWinsForStatusAndDisabledTransient(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cooldown string
		want     time.Duration
	}{
		{name: "longer than status default", cooldown: "1h", want: time.Hour},
		{name: "shorter than status default", cooldown: "1m", want: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) { assertExplicitRequestScopedCooldown(t, tc.cooldown, tc.want) })
	}
}

func assertExplicitRequestScopedCooldown(t *testing.T, configured string, want time.Duration) {
	t.Helper()
	previousCooling := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooling) })
	previousTransient := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	newAuth := func(id string) *Auth {
		return &Auth{
			ID:       id,
			Provider: "claude",
			Status:   StatusActive,
			Metadata: map[string]any{
				"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
					Status:   402,
					Match:    []string{"explicit payment cooldown"},
					Action:   RequestScopedActionStopAndCooldown,
					Cooldown: configured,
				}},
			},
		}
	}

	matchedErr := requestScopedCooldownError{status: 402, body: "explicit payment cooldown"}
	action, cooldown, okAction := matchRequestScopedErrorAction(newAuth("probe"), matchedErr, nil)
	if !okAction || action != RequestScopedActionStopAndCooldown || cooldown != want {
		t.Fatalf("matchRequestScopedErrorAction() = %q, %v, %v; want stop-and-cooldown, %v, true", action, cooldown, okAction, want)
	}

	modelManager := NewManager(nil, nil, nil)
	modelAuth := newAuth("explicit-model-cooldown-" + configured)
	model := "claude-3"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(modelAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(modelAuth.ID) })
	if _, errRegister := modelManager.Register(context.Background(), modelAuth); errRegister != nil {
		t.Fatalf("register model auth: %v", errRegister)
	}
	modelResult := Result{AuthID: modelAuth.ID, Provider: "claude", Model: model, Error: resultErrorFromError(matchedErr)}
	applyRequestScopedActionToResult(action, cooldown, okAction, &modelResult)
	modelManager.MarkResult(context.Background(), modelResult)
	modelSnapshot, _ := modelManager.GetByID(modelAuth.ID)
	modelState := modelSnapshot.ModelStates[model]
	if modelState == nil || !modelState.Unavailable || modelState.NextRetryAfter.Before(time.Now().Add(want-5*time.Second)) || modelState.NextRetryAfter.After(time.Now().Add(want+5*time.Second)) {
		t.Fatalf("model explicit cooldown = %#v; want about %v and unavailable", modelState, want)
	}

	authManager := NewManager(nil, nil, nil)
	authOnly := newAuth("explicit-auth-cooldown-" + configured)
	if _, errRegister := authManager.Register(context.Background(), authOnly); errRegister != nil {
		t.Fatalf("register auth-only auth: %v", errRegister)
	}
	authResult := Result{AuthID: authOnly.ID, Provider: "claude", Error: resultErrorFromError(matchedErr)}
	applyRequestScopedActionToResult(action, cooldown, okAction, &authResult)
	authManager.MarkResult(context.Background(), authResult)
	authSnapshot, _ := authManager.GetByID(authOnly.ID)
	if !authSnapshot.Unavailable || authSnapshot.NextRetryAfter.Before(time.Now().Add(want-5*time.Second)) || authSnapshot.NextRetryAfter.After(time.Now().Add(want+5*time.Second)) {
		t.Fatalf("auth explicit cooldown = unavailable %v, next %v; want about %v", authSnapshot.Unavailable, authSnapshot.NextRetryAfter, want)
	}
}
