package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestMarkResult_429SubSecondRetryAfter_EnforcesMinimumCooldownFloor(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-gemini-subsecond",
		Provider: "google",
	}

	model := "gemini-3.1-flash-image"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	subSecond := 708 * time.Millisecond
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "google",
		Model:      model,
		Success:    false,
		RetryAfter: &subSecond,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("expected NextRetryAfter to be set")
	}

	// Sub-second RetryAfter (e.g. 708ms) must be clamped to at least minQuotaCooldownFloor (10s)
	// to prevent instant unblocking and multi-round retry storms across credentials.
	minExpected := now.Add(10 * time.Second)
	if state.NextRetryAfter.Before(minExpected) {
		t.Fatalf("expected NextRetryAfter >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, state.NextRetryAfter, state.NextRetryAfter.Sub(now))
	}
	if state.Quota.NextRecoverAt.Before(minExpected) {
		t.Fatalf("expected Quota.NextRecoverAt >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, state.Quota.NextRecoverAt, state.Quota.NextRecoverAt.Sub(now))
	}
}

func TestMarkResult_429LongerRetryAfter_PreservedAboveFloor(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-gemini-longer",
		Provider: "google",
	}

	model := "gemini-3.1-flash-image"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	longDuration := 5 * time.Minute
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "google",
		Model:      model,
		Success:    false,
		RetryAfter: &longDuration,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}

	expected := now.Add(longDuration)
	if state.NextRetryAfter.Before(expected.Add(-time.Second)) || state.NextRetryAfter.After(expected.Add(time.Second)) {
		t.Fatalf("expected NextRetryAfter ~ %v, got %v", expected, state.NextRetryAfter)
	}
}

func TestApplyAuthFailureState_429SubSecondRetryAfter_EnforcesMinimumCooldownFloor(t *testing.T) {
	now := time.Now()
	subSecond := 708 * time.Millisecond
	auth := &Auth{ID: "auth-level-subsecond"}
	quotaErr := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    "RESOURCE_EXHAUSTED",
	}

	applyAuthFailureState(auth, quotaErr, &subSecond, now, false)

	if auth.NextRetryAfter.IsZero() {
		t.Fatal("expected NextRetryAfter to be set")
	}
	minExpected := now.Add(10 * time.Second)
	if auth.NextRetryAfter.Before(minExpected) {
		t.Fatalf("expected auth NextRetryAfter >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, auth.NextRetryAfter, auth.NextRetryAfter.Sub(now))
	}
	if auth.Quota.NextRecoverAt.Before(minExpected) {
		t.Fatalf("expected auth Quota.NextRecoverAt >= %v (at least 10s cooldown floor), got %v (diff %v)",
			minExpected, auth.Quota.NextRecoverAt, auth.Quota.NextRecoverAt.Sub(now))
	}
}

func TestManager_Execute_429SubSecondRetryAfter_PreventsRetryStorm(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(5, 5*time.Second, 6)

	subSecond := 708 * time.Millisecond
	executor := &authFallbackExecutor{
		id: "google",
		executeErrors: map[string]error{
			"auth-storm-1": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: subSecond,
			},
			"auth-storm-2": &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota exhausted",
				retryAfter: subSecond,
			},
		},
	}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	model := "gemini-3.1-flash-image"

	for _, id := range []string{"auth-storm-1", "auth-storm-2"} {
		auth := &Auth{
			ID:       id,
			Provider: "google",
		}
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register returned error: %v", errRegister)
		}
		reg.RegisterClient(id, "google", []*registry.ModelInfo{{ID: model}})
		defer reg.UnregisterClient(id)
	}

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := manager.Execute(context.Background(), []string{"google"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	// Each account should be attempted once within round 0.
	// It should NOT enter a tight loop retrying rounds because cooldown (>= 10s) exceeds maxWait (5s).
	calls := executor.ExecuteCalls()
	if len(calls) != 2 {
		t.Fatalf("execute calls = %d, want exactly 2 (initial round only, no retry storm)", len(calls))
	}
}

func TestClosestCooldownWaitWithAttempted_ExpiredCooldownDoesNotTriggerZeroWaitForAttemptedAuth(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "gemini-3.1-flash-image"
	auth := &Auth{
		ID:       "auth-attempted-expired",
		Provider: "google",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	eligibility := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})

	// Case 1: auth was NOT attempted in the failed round (untried credential).
	// Because its cooldown is expired, it should be immediately available (wait = 0, found = true).
	waitUntried, foundUntried := manager.closestCooldownWaitWithAttempted([]string{"google"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, nil)
	if !foundUntried || waitUntried != 0 {
		t.Fatalf("untried credential: wait = %v, found = %t; want (0, true)", waitUntried, foundUntried)
	}

	// Case 2: auth WAS attempted in the failed round that returned 429.
	// Even though its individual cooldown has expired, it must NOT trigger an immediate zero-wait retry round.
	// It must enforce a cooldown floor of at least minQuotaCooldownFloor (10s).
	attempted := map[string]struct{}{auth.ID: {}}
	waitAttempted, foundAttempted := manager.closestCooldownWaitWithAttempted([]string{"google"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !foundAttempted {
		t.Fatal("expected candidate to be found for retry after cooldown")
	}
	if waitAttempted < minQuotaCooldownFloor {
		t.Fatalf("attempted credential after 429: wait = %v, want >= %v (must not be zero-wait)", waitAttempted, minQuotaCooldownFloor)
	}
}

type slowPoolSimulatedExecutor struct {
	m          *Manager
	model      string
	authIDs    []string
	calls      []string
	executeErr error
	mu         sync.Mutex
}

func (e *slowPoolSimulatedExecutor) Identifier() string { return "google" }

func (e *slowPoolSimulatedExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID)
	e.mu.Unlock()

	// When auth-slow-2 executes, auth-slow-1 has already failed and been marked.
	// We mutate auth-slow-1 directly in manager.auths to simulate that the pool iteration
	// was slow enough that auth-slow-1's cooldown expired in the past before round 0 finished.
	if auth.ID == e.authIDs[1] && e.m != nil {
		e.m.mu.Lock()
		if a1 := e.m.auths[e.authIDs[0]]; a1 != nil && a1.ModelStates[e.model] != nil {
			expired := time.Now().Add(-5 * time.Second)
			a1.ModelStates[e.model].NextRetryAfter = expired
			a1.ModelStates[e.model].Quota.NextRecoverAt = expired
		}
		e.m.mu.Unlock()
	}
	return cliproxyexecutor.Response{}, e.executeErr
}

func (*slowPoolSimulatedExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (*slowPoolSimulatedExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, nil
}

func (*slowPoolSimulatedExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (*slowPoolSimulatedExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_Execute_SlowPoolExpiredCooldown_PreventsRetryStorm(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(5, 5*time.Second, 6)

	model := "gemini-3.1-flash-image"
	authIDs := []string{"auth-slow-1", "auth-slow-2"}

	subSecond := 708 * time.Millisecond
	executor := &slowPoolSimulatedExecutor{
		m:       manager,
		model:   model,
		authIDs: authIDs,
		executeErr: &retryAfterStatusError{
			status:     http.StatusTooManyRequests,
			message:    "quota exhausted",
			retryAfter: subSecond,
		},
	}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, id := range authIDs {
		auth := &Auth{ID: id, Provider: "google"}
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("Register error: %v", errRegister)
		}
		reg.RegisterClient(id, "google", []*registry.ModelInfo{{ID: model}})
		defer reg.UnregisterClient(id)
	}

	req := cliproxyexecutor.Request{Model: model}
	_, errExecute := manager.Execute(context.Background(), []string{"google"}, req, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected execute error")
	}
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("execute status = %d, want %d", statusCodeFromError(errExecute), http.StatusTooManyRequests)
	}

	// Confirm that auth-slow-1's cooldown genuinely expired in the past before round 0 completed.
	updatedA1, ok := manager.GetByID(authIDs[0])
	if !ok || updatedA1 == nil || updatedA1.ModelStates[model] == nil {
		t.Fatal("expected auth-slow-1 state to exist")
	}
	if !updatedA1.ModelStates[model].NextRetryAfter.Before(time.Now()) {
		t.Fatalf("expected auth-slow-1 NextRetryAfter to be expired in past, got %v", updatedA1.ModelStates[model].NextRetryAfter)
	}

	// Even though auth-slow-1's cooldown expired before round 0 finished,
	// because both auths were attempted in round 0 and failed with 429,
	// they require a minimum cooldown floor (>= 10s) which exceeds maxWait (5s).
	// Exactly 2 calls (round 0 only) should occur. No retry storm!
	executor.mu.Lock()
	callCount := len(executor.calls)
	executor.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("executor calls = %d, want exactly 2 (round 0 only, calls=%v)", callCount, executor.calls)
	}
}

func TestManager_ShouldRetryAfterError_429EnforcesCooldownWaitEvenWithLargeMaxWait(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "gemini-3.1-flash-image"
	auth := &Auth{
		ID:       "auth-large-maxwait",
		Provider: "google",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "google", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	opts := cliproxyexecutor.Options{}
	err429 := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "RESOURCE_EXHAUSTED"}
	attempted := map[string]struct{}{auth.ID: {}}

	// With maxWait = 30s (which allows waiting up to 30s):
	// Because auth was attempted and failed with 429, it must return wait >= 10s (NOT wait = 0).
	wait, shouldRetry := manager.shouldRetryAfterErrorWithAttempted(context.Background(), opts, err429, 0, []string{"google"}, model, 30*time.Second, -1, 5, attempted)
	if !shouldRetry {
		t.Fatal("expected shouldRetry = true when maxWait (30s) >= cooldown floor (10s)")
	}
	if wait < minQuotaCooldownFloor {
		t.Fatalf("wait = %v, want >= %v (must enforce positive cooldown wait, not zero-wait loop)", wait, minQuotaCooldownFloor)
	}
}

func TestClosestCooldownWaitWithAttempted_RespectsManagerAndProviderCoolingOverrides(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	now := time.Now()
	expired := now.Add(-5 * time.Second)

	model := "compat-model-override"
	auth := &Auth{
		ID:       "auth-compat-override",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"provider_key": "custom-llm",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired},
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "openai-compatibility", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	attempted := map[string]struct{}{auth.ID: {}}
	eligibility := authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})

	// 1. Without provider override (cooling enabled): attempted auth after 429 enforces wait >= 10s.
	wait, found := manager.closestCooldownWaitWithAttempted([]string{"openai-compatibility"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !found || wait < minQuotaCooldownFloor {
		t.Fatalf("cooling enabled: wait = %v, want >= %v", wait, minQuotaCooldownFloor)
	}

	// 2. With provider override setting disable_cooling = true:
	disableCooling := true
	manager.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:           "custom-llm",
			DisableCooling: &disableCooling,
		}},
	})

	// Now cooling is disabled via provider override: attempted auth should allow immediate retry (wait = 0, found = true).
	waitDisabled, foundDisabled := manager.closestCooldownWaitWithAttempted([]string{"openai-compatibility"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !foundDisabled || waitDisabled != 0 {
		t.Fatalf("provider cooling disabled: wait = %v, found = %t; want (0, true)", waitDisabled, foundDisabled)
	}
}
