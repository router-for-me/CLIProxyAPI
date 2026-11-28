package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type retryCancelExecutor struct {
	schedulerTestExecutor
	calls int
}

func (e *retryCancelExecutor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{}, &Error{
		Code:       "rate_limit",
		Message:    "upstream rate limit",
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}
}

func TestExecuteRetryRoundPickCanceledReturnsClientCancellationNotLastError(t *testing.T) {
	rateLimitErr := &Error{
		Code:       "rate_limit",
		Message:    "upstream rate limit",
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}
	if shouldReturnLastErrorOnPickFailure(false, rateLimitErr, context.Canceled) {
		t.Fatal("shouldReturnLastErrorOnPickFailure() = true for context.Canceled, want false")
	}
	if shouldReturnLastErrorOnPickFailure(false, rateLimitErr, context.DeadlineExceeded) {
		t.Fatal("shouldReturnLastErrorOnPickFailure() = true for context.DeadlineExceeded, want false")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerSchedulerModels(t, "test", "issue8537-m1", "issue8537-auth-1", "issue8537-auth-2")
	scheduler := &fakePluginScheduler{
		pick: func(pickCtx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			if len(req.Candidates) == 2 {
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "issue8537-auth-1"}, true, nil
			}
			// Second round: client cancels during scheduler.pick.
			cancel()
			return pluginapi.SchedulerPickResponse{}, true, pickCtx.Err()
		},
	}

	manager := NewManager(nil, nil, nil)
	manager.SetPluginScheduler(scheduler)
	exec := &retryCancelExecutor{schedulerTestExecutor: schedulerTestExecutor{provider: "test"}}
	manager.RegisterExecutor(exec)
	if _, errReg := manager.Register(context.Background(), &Auth{ID: "issue8537-auth-1", Provider: "test", Status: StatusActive}); errReg != nil {
		t.Fatalf("Register auth-1: %v", errReg)
	}
	if _, errReg := manager.Register(context.Background(), &Auth{ID: "issue8537-auth-2", Provider: "test", Status: StatusActive}); errReg != nil {
		t.Fatalf("Register auth-2: %v", errReg)
	}

	// Verify executeMixedOnce directly does not mask context.Canceled with the previous attempt's 429.
	_, errOnce := manager.executeMixedOnce(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "issue8537-m1"}, cliproxyexecutor.Options{}, 0, 0, 0)
	if !errors.Is(errOnce, context.Canceled) {
		t.Fatalf("executeMixedOnce() error = %v, want context.Canceled", errOnce)
	}
	if status := clienterror.HTTPStatusFromError(errOnce); status != clienterror.StatusClientClosedRequest {
		t.Fatalf("HTTPStatusFromError(errOnce) = %d, want %d (499)", status, clienterror.StatusClientClosedRequest)
	}
}

func TestPickViaPluginSchedulerReturnsContextCanceledWhenPluginRejectsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registerSchedulerModels(t, "test", "issue8537-m2", "issue8537-auth-3")
	scheduler := &fakePluginScheduler{
		pick: func(_ context.Context, _ pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			cancel()
			return pluginapi.SchedulerPickResponse{
				Handled:      true,
				Reject:       true,
				RejectCode:   "hold_aborted",
				RejectReason: "wait aborted",
			}, true, nil
		},
	}

	manager := NewManager(nil, nil, nil)
	manager.SetPluginScheduler(scheduler)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "test"})
	if _, errReg := manager.Register(context.Background(), &Auth{ID: "issue8537-auth-3", Provider: "test", Status: StatusActive}); errReg != nil {
		t.Fatalf("Register auth-3: %v", errReg)
	}

	_, errExec := manager.Execute(ctx, []string{"test"}, cliproxyexecutor.Request{Model: "issue8537-m2"}, cliproxyexecutor.Options{})
	if !errors.Is(errExec, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", errExec)
	}
	if status := clienterror.HTTPStatusFromError(errExec); status != clienterror.StatusClientClosedRequest {
		t.Fatalf("HTTPStatusFromError(err) = %d, want %d (499)", status, clienterror.StatusClientClosedRequest)
	}
}
