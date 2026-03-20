package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type schedulerProviderTestExecutor struct {
	provider string
}

func (e schedulerProviderTestExecutor) Identifier() string { return e.provider }

func (e schedulerProviderTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e schedulerProviderTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e schedulerProviderTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e schedulerProviderTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_RefreshSchedulerEntry_RebuildsSupportedModelSetAfterModelRegistration(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		prime func(*Manager, *Auth) error
	}{
		{
			name: "register",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				return errRegister
			},
		},
		{
			name: "update",
			prime: func(manager *Manager, auth *Auth) error {
				_, errRegister := manager.Register(ctx, auth)
				if errRegister != nil {
					return errRegister
				}
				updated := auth.Clone()
				updated.Metadata = map[string]any{"updated": true}
				_, errUpdate := manager.Update(ctx, updated)
				return errUpdate
			},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			auth := &Auth{
				ID:       "refresh-entry-" + testCase.name,
				Provider: "gemini",
			}
			if errPrime := testCase.prime(manager, auth); errPrime != nil {
				t.Fatalf("prime auth %s: %v", testCase.name, errPrime)
			}

			registerSchedulerModels(t, "gemini", "scheduler-refresh-model", auth.ID)

			gotID, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr == nil {
				t.Fatalf("pickSingle() before refresh error = %v, want auth_not_found", errPick)
			}
			if authErr.Code != "auth_not_found" {
				t.Fatalf("pickSingle() before refresh code = %q, want %q", authErr.Code, "auth_not_found")
			}
			if gotID != "" {
				t.Fatalf("pickSingle() before refresh authID = %q, want empty", gotID)
			}

			manager.RefreshSchedulerEntry(auth.ID)

			gotID, errPick = manager.scheduler.pickSingle(ctx, "gemini", "scheduler-refresh-model", cliproxyexecutor.Options{}, nil)
			if errPick != nil {
				t.Fatalf("pickSingle() after refresh error = %v", errPick)
			}
			if gotID != auth.ID {
				t.Fatalf("pickSingle() after refresh authID = %q, want %q", gotID, auth.ID)
			}
		})
	}
}

func TestManager_PickNext_RebuildsSchedulerAfterModelCooldownError(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "gemini"})

	registerSchedulerModels(t, "gemini", "scheduler-cooldown-rebuild-model", "cooldown-stale-old")

	oldAuth := &Auth{
		ID:       "cooldown-stale-old",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, oldAuth); errRegister != nil {
		t.Fatalf("register old auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   oldAuth.ID,
		Provider: "gemini",
		Model:    "scheduler-cooldown-rebuild-model",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})

	newAuth := &Auth{
		ID:       "cooldown-stale-new",
		Provider: "gemini",
	}
	if _, errRegister := manager.Register(ctx, newAuth); errRegister != nil {
		t.Fatalf("register new auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(newAuth.ID, "gemini", []*registry.ModelInfo{{ID: "scheduler-cooldown-rebuild-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(newAuth.ID)
	})

	gotID, errPick := manager.scheduler.pickSingle(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	var cooldownErr *modelCooldownError
	if !errors.As(errPick, &cooldownErr) {
		t.Fatalf("pickSingle() before sync error = %v, want modelCooldownError", errPick)
	}
	if gotID != "" {
		t.Fatalf("pickSingle() before sync authID = %q, want empty", gotID)
	}

	got, executor, errPick := manager.pickNext(ctx, "gemini", "scheduler-cooldown-rebuild-model", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if executor == nil {
		t.Fatal("pickNext() executor = nil")
	}
	if got == nil || got.ID != newAuth.ID {
		t.Fatalf("pickNext() auth = %v, want %q", got, newAuth.ID)
	}
}

func TestManager_ClosestCooldownWait_UsesSchedulerBlockedQueueWithRetryOverride(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	registerSchedulerModels(t, "gemini", "cooldown-lookup-model", "cooldown-skip", "cooldown-keep")

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       "cooldown-skip",
		Provider: "gemini",
		Metadata: map[string]any{"request_retry": 0},
	}); errRegister != nil {
		t.Fatalf("register cooldown-skip: %v", errRegister)
	}
	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       "cooldown-keep",
		Provider: "gemini",
		Metadata: map[string]any{"request_retry": 2},
	}); errRegister != nil {
		t.Fatalf("register cooldown-keep: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:     "cooldown-skip",
		Provider:   "gemini",
		Model:      "cooldown-lookup-model",
		Success:    false,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		RetryAfter: durationPtr(20 * time.Millisecond),
	})
	manager.MarkResult(ctx, Result{
		AuthID:     "cooldown-keep",
		Provider:   "gemini",
		Model:      "cooldown-lookup-model",
		Success:    false,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		RetryAfter: durationPtr(60 * time.Millisecond),
	})

	wait, found := manager.closestCooldownWait([]string{"gemini"}, "cooldown-lookup-model", 0)
	if !found {
		t.Fatal("closestCooldownWait() found = false, want true")
	}
	if wait < 40*time.Millisecond {
		t.Fatalf("closestCooldownWait() = %v, want wait close to second auth retry window", wait)
	}
	if wait > 2*time.Second {
		t.Fatalf("closestCooldownWait() = %v, unexpectedly large", wait)
	}

	wait, found = manager.closestCooldownWait([]string{"gemini"}, "cooldown-lookup-model", 2)
	if found {
		t.Fatalf("closestCooldownWait() at attempt 2 found = true, want false (all retry budgets exhausted), wait=%v", wait)
	}
}

func durationPtr(value time.Duration) *time.Duration {
	return &value
}
