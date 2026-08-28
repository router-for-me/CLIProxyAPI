package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPrepareErrorPropagatesTransientRateLimit(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := time.Duration(observedExhaustedQuotaHint)
	paths := []struct {
		name string
		run  func(context.Context, *Manager, string) error
	}{
		{
			name: "execute",
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count tokens",
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errStream := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			hook := &resultCaptureHook{}
			executor := &claudeCancellationTestExecutor{
				prepareFn: func(context.Context, *Auth) (*Auth, error) {
					return nil, streamTransientRateLimitError{retryAfter: hint}
				},
			}
			manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)

			before := time.Now()
			if errRun := path.run(context.Background(), manager, model); errRun == nil {
				t.Fatal("expected prepare 429 to fail the request")
			}
			if executor.executeCalls.Load()+executor.countCalls.Load()+executor.streamCalls.Load() != 0 {
				t.Fatal("executor ran after request preparation failed")
			}

			results := hook.Results()
			if len(results) != 1 {
				t.Fatalf("hook results = %d, want 1", len(results))
			}
			got := results[0]
			if !got.TransientRateLimit {
				t.Fatal("expected oauth token-refresh 429 during prepare to be transient so the conductor skips the quota ladder")
			}
			if got.RetryAfter == nil || *got.RetryAfter != hint {
				t.Fatalf("RetryAfter = %v, want provider hint %v", got.RetryAfter, hint)
			}

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil || updated.ModelStates[model] == nil {
				t.Fatalf("expected model state after prepare 429")
			}
			state := updated.ModelStates[model]
			if state.Quota.BackoffLevel != 0 {
				t.Fatalf("expected BackoffLevel 0 on the transient prepare-429 path, got %d", state.Quota.BackoffLevel)
			}
			if state.Quota.Exceeded {
				t.Fatal("transient prepare 429 must not set Quota.Exceeded")
			}
			if gotWindow := state.NextRetryAfter.Sub(before); gotWindow < transientRateLimitMinimum {
				t.Fatalf("prepare 429 took the quota ladder: NextRetryAfter delta %v, want at least the 10s transient floor", gotWindow)
			}
		})
	}
}

func TestPrepareErrorWithoutTransientKeepsQuotaLadder(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := time.Duration(observedExhaustedQuotaHint)
	hook := &resultCaptureHook{}
	executor := &claudeCancellationTestExecutor{
		prepareFn: func(context.Context, *Auth) (*Auth, error) {
			return nil, &retryAfterStatusError{
				status:     http.StatusTooManyRequests,
				message:    "quota",
				retryAfter: hint,
			}
		},
	}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)

	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("expected unclassified prepare 429 to fail the request")
	}

	results := hook.Results()
	if len(results) != 1 {
		t.Fatalf("hook results = %d, want 1", len(results))
	}
	if results[0].TransientRateLimit {
		t.Fatal("unclassified prepare 429 must not become transient")
	}
	if results[0].RetryAfter == nil || *results[0].RetryAfter != hint {
		t.Fatalf("RetryAfter = %v, want provider hint %v", results[0].RetryAfter, hint)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatal("expected model state after unclassified prepare 429")
	}
	state := updated.ModelStates[model]
	if state.Quota.BackoffLevel != 1 {
		t.Fatalf("expected BackoffLevel 1 for unclassified prepare 429, got %d", state.Quota.BackoffLevel)
	}
}

func TestPrepareGenericErrorIsNotTransient(t *testing.T) {
	hook := &resultCaptureHook{}
	executor := &claudeCancellationTestExecutor{
		prepareFn: func(context.Context, *Auth) (*Auth, error) {
			return nil, errors.New("missing project_id")
		},
	}
	manager, _, model := newClaudeCancellationTestManager(t, executor, hook)

	if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("expected generic prepare error to fail the request")
	}

	results := hook.Results()
	if len(results) != 1 {
		t.Fatalf("hook results = %d, want 1", len(results))
	}
	if results[0].TransientRateLimit {
		t.Fatal("generic prepare error must not be classified as transient")
	}
	if results[0].RetryAfter != nil {
		t.Fatalf("RetryAfter = %v, want nil", results[0].RetryAfter)
	}
}

func TestHomePrepareErrorPropagatesTransientRateLimit(t *testing.T) {
	hint := time.Duration(observedExhaustedQuotaHint)
	paths := []struct {
		name string
		run  func(*Manager, context.Context) error
	}{
		{
			name: "Execute",
			run: func(manager *Manager, ctx context.Context) error {
				_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "Count",
			run: func(manager *Manager, ctx context.Context) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "Stream",
			run: func(manager *Manager, ctx context.Context) error {
				result, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					return errStream
				}
				if result != nil {
					for range result.Chunks {
					}
				}
				return nil
			},
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			store := &requestPrepareStore{}
			hook := &resultCaptureHook{}
			executor := &requestPrepareExecutor{prepareErr: streamTransientRateLimitError{retryAfter: hint}}
			manager := NewManager(store, nil, hook)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.PublishHomeDispatch(&homeRequestPrepareDispatcher{}, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)
			localAuth := &Auth{
				ID:       "same-id",
				Provider: "antigravity",
				Status:   StatusActive,
				Metadata: map[string]any{"access_token": "local-token", "source": "local"},
			}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), localAuth); errRegister != nil {
				t.Fatalf("register local auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(localAuth.ID, localAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(localAuth.ID) })

			if errRun := path.run(manager, context.Background()); errRun == nil {
				t.Fatal("expected home prepare 429 to fail the request")
			}

			results := hook.Results()
			if len(results) != 1 {
				t.Fatalf("hook results = %#v, want exactly one ephemeral result", results)
			}
			got := results[0]
			if !got.TransientRateLimit {
				t.Fatal("expected home prepare 429 to reach the conductor as a transient rate limit")
			}
			if got.RetryAfter == nil || *got.RetryAfter != hint {
				t.Fatalf("RetryAfter = %v, want provider hint %v", got.RetryAfter, hint)
			}

			current, ok := manager.GetByID(localAuth.ID)
			if !ok || current == nil {
				t.Fatal("local auth disappeared")
			}
			if current.ModelStates != nil {
				if state := current.ModelStates["test-model"]; state != nil && state.Quota.BackoffLevel != 0 {
					t.Fatalf("Home prepare must not mutate local BackoffLevel, got %d", state.Quota.BackoffLevel)
				}
			}
		})
	}
}
