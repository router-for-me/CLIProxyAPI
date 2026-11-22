package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7716CancelPrepareExecutor simulates a non-Claude OAuth provider whose
// request-auth preparation (e.g. token refresh) observes a client-side
// cancellation and returns the context error. The executor tracks how many
// credentials were prepared and how many upstream calls ran so tests can
// assert the credential loop stops immediately on cancellation.
type issue7716CancelPrepareExecutor struct {
	prepareCalls atomic.Int32
	executeCalls atomic.Int32
	countCalls   atomic.Int32
	streamCalls  atomic.Int32

	cancel context.CancelFunc
}

func (*issue7716CancelPrepareExecutor) Identifier() string { return "antigravity" }

func (*issue7716CancelPrepareExecutor) ShouldPrepareRequestAuth(*Auth) bool { return true }

func (e *issue7716CancelPrepareExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	if e.cancel != nil {
		e.cancel()
	}
	return auth, ctx.Err()
}

func (e *issue7716CancelPrepareExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7716CancelPrepareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *issue7716CancelPrepareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *issue7716CancelPrepareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7716CancelPrepareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// newIssue7716Manager builds a manager with a pool of three Antigravity OAuth
// credentials, mirroring the reproduction described in issue #7716.
func newIssue7716Manager(t *testing.T, executor *issue7716CancelPrepareExecutor, hook Hook, model string, authIDs []string) *Manager {
	t.Helper()
	manager := NewManager(nil, nil, hook)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	for _, authID := range authIDs {
		auth := &Auth{
			ID:       authID,
			Provider: "antigravity",
			Attributes: map[string]string{
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{
				"access_token":  fmt.Sprintf("access-%s", authID),
				"refresh_token": fmt.Sprintf("refresh-%s", authID),
				"request_retry": float64(0),
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	}
	t.Cleanup(func() {
		for _, authID := range authIDs {
			registry.GetGlobalRegistry().UnregisterClient(authID)
		}
	})
	return manager
}

// TestIssue7716PrepareCancellationStopsCredentialLoop asserts that a client
// cancellation raised while prepareRequestAuth runs for a non-Claude provider
// aborts the credential selection loop immediately: the context error is
// returned, no failure result is recorded for the credential, and no further
// credentials are picked or prepared.
func TestIssue7716PrepareCancellationStopsCredentialLoop(t *testing.T) {
	model := "issue7716-model-" + uuid.NewString()
	authIDs := []string{
		"issue7716-auth-1-" + uuid.NewString(),
		"issue7716-auth-2-" + uuid.NewString(),
		"issue7716-auth-3-" + uuid.NewString(),
	}

	tests := []struct {
		name      string
		run       func(context.Context, *Manager, string) error
		wantCalls func(*issue7716CancelPrepareExecutor) int32
	}{
		{
			name: "execute",
			run: func(ctx context.Context, manager *Manager, routeModel string) error {
				_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
				return errExecute
			},
			wantCalls: func(e *issue7716CancelPrepareExecutor) int32 { return e.executeCalls.Load() },
		},
		{
			name: "count tokens",
			run: func(ctx context.Context, manager *Manager, routeModel string) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{})
				return errCount
			},
			wantCalls: func(e *issue7716CancelPrepareExecutor) int32 { return e.countCalls.Load() },
		},
		{
			name: "stream",
			run: func(ctx context.Context, manager *Manager, routeModel string) error {
				_, errStream := manager.ExecuteStream(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: routeModel}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
			wantCalls: func(e *issue7716CancelPrepareExecutor) int32 { return e.streamCalls.Load() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			executor := &issue7716CancelPrepareExecutor{cancel: cancel}
			hook := &resultCaptureHook{}
			manager := newIssue7716Manager(t, executor, hook, model, authIDs)

			errRun := tt.run(ctx, manager, model)
			if !errors.Is(errRun, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", errRun)
			}
			if got := executor.prepareCalls.Load(); got != 1 {
				t.Fatalf("PrepareRequestAuth calls = %d, want 1 (no further credentials picked)", got)
			}
			if got := tt.wantCalls(executor); got != 0 {
				t.Fatalf("upstream calls = %d, want 0", got)
			}
			if results := hook.Results(); len(results) != 0 {
				t.Fatalf("recorded %d results for client cancellation, want 0", len(results))
			}
			for _, authID := range authIDs {
				auth, ok := manager.GetByID(authID)
				if !ok || auth == nil {
					t.Fatalf("GetByID(%q) did not return auth", authID)
				}
				if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
					t.Fatalf("auth %s cooled on client cancellation: unavailable=%t next=%v", authID, auth.Unavailable, auth.NextRetryAfter)
				}
			}
		})
	}
}

// TestIssue7716PrepareDeadlineExceededStopsCredentialLoop asserts the same
// behavior for context.DeadlineExceeded raised during preparation, which
// claudeOAuthRequestCancellation never intercepts even for Claude.
func TestIssue7716PrepareDeadlineExceededStopsCredentialLoop(t *testing.T) {
	model := "issue7716-model-" + uuid.NewString()
	authIDs := []string{
		"issue7716-auth-1-" + uuid.NewString(),
		"issue7716-auth-2-" + uuid.NewString(),
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	executor := &issue7716CancelPrepareExecutor{} // no explicit cancel; ctx already expired
	hook := &resultCaptureHook{}
	manager := newIssue7716Manager(t, executor, hook, model, authIDs)

	_, errExecute := manager.Execute(ctx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if !errors.Is(errExecute, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", errExecute)
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 1 (no further credentials picked)", got)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
	if results := hook.Results(); len(results) != 0 {
		t.Fatalf("recorded %d results for client deadline, want 0", len(results))
	}
}
