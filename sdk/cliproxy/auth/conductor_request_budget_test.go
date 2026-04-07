package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type budgetBlockingExecutor struct {
	id    string
	calls atomic.Int32
}

func (e *budgetBlockingExecutor) Identifier() string {
	return e.id
}

func (e *budgetBlockingExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	<-ctx.Done()
	return cliproxyexecutor.Response{}, ctx.Err()
}

func (e *budgetBlockingExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (e *budgetBlockingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *budgetBlockingExecutor) CountTokens(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	<-ctx.Done()
	return cliproxyexecutor.Response{}, ctx.Err()
}

func (e *budgetBlockingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func newRequestBudgetTestManager(t *testing.T, budget time.Duration) (*Manager, *budgetBlockingExecutor) {
	t.Helper()

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	manager.SetRequestBudget(budget)

	executor := &budgetBlockingExecutor{id: "claude"}
	manager.RegisterExecutor(executor)

	auth := &Auth{ID: "budget-auth", Provider: "claude"}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "budget-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	return manager, executor
}

func TestManagerExecute_RequestBudgetExceededReturnsGatewayTimeout(t *testing.T) {
	manager, executor := newRequestBudgetTestManager(t, 40*time.Millisecond)

	_, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "budget-model"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T (%v)", err, err)
	}
	if authErr.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want=%d", authErr.HTTPStatus, http.StatusGatewayTimeout)
	}
	if authErr.Code != "upstream_timeout" {
		t.Fatalf("code=%q want=%q", authErr.Code, "upstream_timeout")
	}
	if calls := executor.calls.Load(); calls != 1 {
		t.Fatalf("calls=%d want=1", calls)
	}
}

func TestManagerExecuteStream_RequestBudgetExceededReturnsGatewayTimeout(t *testing.T) {
	manager, executor := newRequestBudgetTestManager(t, 40*time.Millisecond)

	_, err := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "budget-model"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	authErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T (%v)", err, err)
	}
	if authErr.HTTPStatus != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want=%d", authErr.HTTPStatus, http.StatusGatewayTimeout)
	}
	if authErr.Code != "upstream_timeout" {
		t.Fatalf("code=%q want=%q", authErr.Code, "upstream_timeout")
	}
	if calls := executor.calls.Load(); calls != 1 {
		t.Fatalf("calls=%d want=1", calls)
	}
}

func TestManagerShouldRetryAfterError_SkipCooldownWhenRemainingBudgetInsufficient(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 30*time.Second, 0)

	model := "budget-model"
	next := time.Now().Add(600 * time.Millisecond)
	auth := &Auth{
		ID:       "budget-auth",
		Provider: "claude",
		ModelStates: map[string]*ModelState{
			model: {
				Unavailable:    true,
				Status:         StatusError,
				NextRetryAfter: next,
			},
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_, _, maxWait := manager.retrySettings()
	wait, shouldRetry := manager.shouldRetryAfterError(ctx, &Error{HTTPStatus: http.StatusInternalServerError, Message: "boom"}, 0, []string{"claude"}, model, maxWait)
	if shouldRetry {
		t.Fatalf("shouldRetry=%v wait=%v, want false", shouldRetry, wait)
	}
	if wait != 0 {
		t.Fatalf("wait=%v want=0", wait)
	}
}

func TestManagerExecute_RequestBudgetDisabledPreservesContextDeadlineError(t *testing.T) {
	manager, _ := newRequestBudgetTestManager(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, err := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: "budget-model"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected deadline exceeded error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %T (%v)", err, err)
	}
	if authErr, ok := err.(*Error); ok && authErr.Code == "upstream_timeout" {
		t.Fatalf("expected native context timeout when request budget disabled, got %v", authErr)
	}
}

func TestManagerExecute_RequestBudgetKeepsShorterCallerDeadline(t *testing.T) {
	manager, _ := newRequestBudgetTestManager(t, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, err := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: "budget-model"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected deadline exceeded error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %T (%v)", err, err)
	}
	if authErr, ok := err.(*Error); ok && authErr.Code == "upstream_timeout" {
		t.Fatalf("expected shorter caller deadline to win, got %v", authErr)
	}
}
