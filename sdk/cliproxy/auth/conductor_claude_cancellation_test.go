package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type claudeCancellationTestExecutor struct {
	prepareFn func(context.Context, *Auth) (*Auth, error)
	executeFn func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	countFn   func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	streamFn  func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error)
	refreshFn func(context.Context, *Auth) (*Auth, error)

	prepareCalls atomic.Int32
	executeCalls atomic.Int32
	countCalls   atomic.Int32
	streamCalls  atomic.Int32
	refreshCalls atomic.Int32
}

func (*claudeCancellationTestExecutor) Identifier() string { return "claude" }

func (e *claudeCancellationTestExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	return e.prepareFn != nil
}

func (e *claudeCancellationTestExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	return e.prepareFn(ctx, auth)
}

func (e *claudeCancellationTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	if e.executeFn != nil {
		return e.executeFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *claudeCancellationTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.countCalls.Add(1)
	if e.countFn != nil {
		return e.countFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *claudeCancellationTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.streamCalls.Add(1)
	if e.streamFn != nil {
		return e.streamFn(ctx, auth)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *claudeCancellationTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	return auth, nil
}

func (*claudeCancellationTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

type claudeRequestScopedCancellation struct{}

func (claudeRequestScopedCancellation) Error() string         { return context.Canceled.Error() }
func (claudeRequestScopedCancellation) Unwrap() error         { return context.Canceled }
func (claudeRequestScopedCancellation) IsRequestScoped() bool { return true }

func newClaudeCancellationTestManager(t *testing.T, executor *claudeCancellationTestExecutor, hook Hook) (*Manager, *Auth, string) {
	t.Helper()
	if hook == nil {
		hook = NoopHook{}
	}
	model := "claude-cancel-model-" + uuid.NewString()
	auth := &Auth{
		ID:         "claude-cancel-auth-" + uuid.NewString(),
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata: map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"request_retry": float64(0),
		},
	}
	manager := NewManager(nil, nil, hook)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	return manager, auth, model
}

func requireClaudeCancellationNeutral(t *testing.T, manager *Manager, authID, model string) {
	t.Helper()
	auth, ok := manager.GetByID(authID)
	if !ok || auth == nil {
		t.Fatalf("GetByID(%q) did not return auth", authID)
	}
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth was cooled: unavailable=%t next=%v", auth.Unavailable, auth.NextRetryAfter)
	}
	if state := auth.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded) {
		t.Fatalf("model was cooled: %#v", state)
	}
}

func TestManagerClaudePrepareCancellationStopsWithoutCooldown(t *testing.T) {
	tests := []struct {
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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			executor := &claudeCancellationTestExecutor{}
			executor.prepareFn = func(ctx context.Context, auth *Auth) (*Auth, error) {
				cancel()
				return auth, ctx.Err()
			}
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

			errExecute := tt.run(ctx, manager, model)
			if !errors.Is(errExecute, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", errExecute)
			}
			if got := executor.prepareCalls.Load(); got != 1 {
				t.Fatalf("PrepareRequestAuth calls = %d, want 1", got)
			}
			if executor.executeCalls.Load()+executor.countCalls.Load()+executor.streamCalls.Load() != 0 {
				t.Fatal("executor ran after request preparation was canceled")
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}

func TestManagerClaudeRefreshCancellationStopsWithoutCooldown(t *testing.T) {
	unauthorized := &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
	tests := []struct {
		name      string
		configure func(*claudeCancellationTestExecutor)
		run       func(context.Context, *Manager, string) error
	}{
		{
			name: "execute",
			configure: func(executor *claudeCancellationTestExecutor) {
				executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, unauthorized
				}
			},
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count tokens",
			configure: func(executor *claudeCancellationTestExecutor) {
				executor.countFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, unauthorized
				}
			},
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			configure: func(executor *claudeCancellationTestExecutor) {
				executor.streamFn = func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
					return nil, unauthorized
				}
			},
			run: func(ctx context.Context, manager *Manager, model string) error {
				_, errStream := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			executor := &claudeCancellationTestExecutor{}
			tt.configure(executor)
			executor.refreshFn = func(ctx context.Context, _ *Auth) (*Auth, error) {
				cancel()
				return nil, ctx.Err()
			}
			manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

			errExecute := tt.run(ctx, manager, model)
			if !errors.Is(errExecute, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", errExecute)
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("Refresh calls = %d, want 1", got)
			}
			if upstreamCalls := executor.executeCalls.Load() + executor.countCalls.Load() + executor.streamCalls.Load(); upstreamCalls != 1 {
				t.Fatalf("upstream calls = %d, want 1", upstreamCalls)
			}
			requireClaudeCancellationNeutral(t, manager, auth.ID, model)
		})
	}
}

func TestManagerClaudeStreamTailCancellationIsAvailabilityNeutral(t *testing.T) {
	source := make(chan cliproxyexecutor.StreamChunk, 1)
	source <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	executor := &claudeCancellationTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{Chunks: source}, nil
		},
	}
	hook := &resultCaptureHook{}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)
	ctx, cancel := context.WithCancel(context.Background())

	stream, errStream := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if chunk := <-stream.Chunks; chunk.Err != nil || string(chunk.Payload) != "first" {
		t.Fatalf("first chunk = %#v", chunk)
	}
	cancel()
	source <- cliproxyexecutor.StreamChunk{Err: claudeRequestScopedCancellation{}}
	close(source)
	for range stream.Chunks {
	}

	results := hook.Results()
	if len(results) != 1 || results[0].Success || results[0].Error == nil {
		t.Fatalf("results = %#v, want one failed cancellation result", results)
	}
	if results[0].Error.Code != requestScopedErrorCode || results[0].Error.StatusCode() != 0 {
		t.Fatalf("cancellation result = %#v, want request-scoped status 0", results[0].Error)
	}
	requireClaudeCancellationNeutral(t, manager, auth.ID, model)
}

func TestManagerClaudeUpstreamFailureStillCoolsCredential(t *testing.T) {
	executor := &claudeCancellationTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "upstream failure"}
		},
	}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusInternalServerError {
		t.Fatalf("Execute() error = %v, want HTTP 500", errExecute)
	}
	got, ok := manager.GetByID(auth.ID)
	if !ok || got == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	state := got.ModelStates[model]
	if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("upstream failure did not cool model: %#v", state)
	}
}

func TestClaudeRequestCancellationDoesNotChangeOtherProviders(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []*Auth{
		{Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}},
		{Provider: "claude", Attributes: map[string]string{"auth_kind": "api_key"}},
	}
	for _, auth := range tests {
		if errCancel := claudeOAuthRequestCancellation(ctx, auth, context.Canceled); errCancel != nil {
			t.Fatalf("auth %#v was classified as Claude OAuth cancellation: %v", auth.Attributes, errCancel)
		}
	}
}
