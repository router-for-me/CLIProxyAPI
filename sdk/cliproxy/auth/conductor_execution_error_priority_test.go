package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalhome "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type retryTerminalPriorityExecutor struct {
	identifier  string
	upstreamErr error
	terminalErr error
	cancel      context.CancelFunc
	calls       atomic.Int32
}

type retryTerminalPriorityHomeDispatcher struct {
	finalPayload []byte
}

func (*retryTerminalPriorityHomeDispatcher) HeartbeatOK() bool { return true }

func (d *retryTerminalPriorityHomeDispatcher) RPopAuth(ctx context.Context, model string, sessionID string, headers http.Header, count int) ([]byte, error) {
	return d.RPopAuthWithRetryRoundConstraints(ctx, model, sessionID, headers, count, 0, nil, "")
}

func (d *retryTerminalPriorityHomeDispatcher) RPopAuthWithRetryRoundConstraints(_ context.Context, _ string, _ string, _ http.Header, _ int, retryRound int, excludedAuthIDs []string, _ string) ([]byte, error) {
	if len(excludedAuthIDs) == 0 {
		authID := "home-retry-a"
		if retryRound > 0 {
			authID = "home-retry-b"
		}
		requestRetry := 1
		return json.Marshal(homeAuthDispatchResponse{
			RequestRetry: &requestRetry,
			Auth: Auth{
				ID:       authID,
				Provider: "home-retry-contract",
				Status:   StatusActive,
			},
		})
	}
	if retryRound == 0 {
		return []byte(`{"error":{"type":"auth_unavailable","message":"a credential is immediately available next round"}}`), nil
	}
	if len(d.finalPayload) > 0 {
		return append([]byte(nil), d.finalPayload...), nil
	}
	return nil, internalhome.ErrAuthNotFound
}

func (*retryTerminalPriorityHomeDispatcher) AbortAmbiguousDispatch() {}

func (e *retryTerminalPriorityExecutor) Identifier() string { return e.identifier }

func (e *retryTerminalPriorityExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.nextError(ctx)
}

func (e *retryTerminalPriorityExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, e.nextError(ctx)
}

func (*retryTerminalPriorityExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *retryTerminalPriorityExecutor) CountTokens(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.nextError(ctx)
}

func (*retryTerminalPriorityExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *retryTerminalPriorityExecutor) nextError(ctx context.Context) error {
	if e.calls.Add(1) == 1 {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return e.upstreamErr
	}
	if e.cancel != nil {
		e.cancel()
	}
	return e.terminalErr
}

func TestManagerRetryPreservesTerminalContextErrorAfterUpstreamFailure(t *testing.T) {
	paths := []struct {
		name   string
		invoke func(context.Context, *Manager, string, string) error
	}{
		{
			name: "execute",
			invoke: func(ctx context.Context, manager *Manager, provider, model string) error {
				_, errExecute := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count-tokens",
			invoke: func(ctx context.Context, manager *Manager, provider, model string) error {
				_, errCount := manager.ExecuteCount(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			invoke: func(ctx context.Context, manager *Manager, provider, model string) error {
				_, errStream := manager.ExecuteStream(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
		},
	}
	terminalCases := []struct {
		name         string
		err          error
		cancelParent bool
	}{
		{name: "canceled", err: context.Canceled, cancelParent: true},
		{name: "deadline-exceeded", err: context.DeadlineExceeded},
	}

	for _, path := range paths {
		for _, terminalCase := range terminalCases {
			t.Run(path.name+"/"+terminalCase.name, func(t *testing.T) {
				provider := "retry-terminal-priority-" + path.name + "-" + terminalCase.name
				model := provider + "-model"
				authID := provider + "-auth"
				upstreamErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "first retry round reached upstream"}

				ctx := context.Background()
				var cancel context.CancelFunc
				if terminalCase.cancelParent {
					ctx, cancel = context.WithCancel(ctx)
					t.Cleanup(cancel)
				}

				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(1, 0, 0)
				executor := &retryTerminalPriorityExecutor{
					identifier:  provider,
					upstreamErr: upstreamErr,
					terminalErr: terminalCase.err,
					cancel:      cancel,
				}
				manager.RegisterExecutor(executor)
				registerRetryRoundLocalAuths(t, manager, provider, model, map[string]int{authID: 1})

				errExecute := path.invoke(ctx, manager, provider, model)
				if !errors.Is(errExecute, terminalCase.err) {
					t.Fatalf("execution error = %v, want %v", errExecute, terminalCase.err)
				}
				if errors.Is(errExecute, upstreamErr) {
					t.Fatalf("execution error = %v, earlier upstream error took priority", errExecute)
				}
				if got := executor.calls.Load(); got != 2 {
					t.Fatalf("executor calls = %d, want one upstream failure and one terminal retry", got)
				}
			})
		}
	}
}

func TestHomeRetryPreservesTerminalContextErrorAfterUpstreamFailure(t *testing.T) {
	paths := []struct {
		name   string
		invoke func(context.Context, *Manager) error
	}{
		{
			name: "execute",
			invoke: func(ctx context.Context, manager *Manager) error {
				_, errExecute := manager.Execute(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count-tokens",
			invoke: func(ctx context.Context, manager *Manager) error {
				_, errCount := manager.ExecuteCount(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			invoke: func(ctx context.Context, manager *Manager) error {
				_, errStream := manager.ExecuteStream(ctx, []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
		},
	}
	terminalCases := []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline-exceeded", err: context.DeadlineExceeded},
	}

	for _, path := range paths {
		for _, terminalCase := range terminalCases {
			t.Run(path.name+"/"+terminalCase.name, func(t *testing.T) {
				upstreamErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "first retry round reached upstream"}
				executor := &retryTerminalPriorityExecutor{
					identifier:  "home-retry-contract",
					upstreamErr: upstreamErr,
					terminalErr: terminalCase.err,
				}
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.SetRetryConfig(1, 0, 0)
				manager.PublishHomeDispatch(&retryTerminalPriorityHomeDispatcher{}, executionregistry.New(), 1)
				manager.RegisterExecutor(executor)

				errExecute := path.invoke(context.Background(), manager)
				if !errors.Is(errExecute, terminalCase.err) {
					t.Fatalf("execution error = %v, want %v", errExecute, terminalCase.err)
				}
				if errors.Is(errExecute, upstreamErr) {
					t.Fatalf("execution error = %v, earlier upstream error took priority", errExecute)
				}
				if got := executor.calls.Load(); got < 2 {
					t.Fatalf("executor calls = %d, want an upstream failure followed by a terminal retry", got)
				}
			})
		}
	}
}

func TestHomePreferredUpstreamErrorPreservesCurrentRetryAfter(t *testing.T) {
	paths := []struct {
		name   string
		invoke func(*Manager) error
	}{
		{
			name: "execute",
			invoke: func(manager *Manager) error {
				_, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errExecute
			},
		},
		{
			name: "count-tokens",
			invoke: func(manager *Manager) error {
				_, errCount := manager.ExecuteCount(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
				return errCount
			},
		},
		{
			name: "stream",
			invoke: func(manager *Manager) error {
				_, errStream := manager.ExecuteStream(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{Stream: true})
				return errStream
			},
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			upstreamErr := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "first retry round reached upstream"}
			internalErr := errors.New("second retry round failed before upstream")
			executor := &retryTerminalPriorityExecutor{
				identifier:  "home-retry-contract",
				upstreamErr: upstreamErr,
				terminalErr: internalErr,
			}
			dispatcher := &retryTerminalPriorityHomeDispatcher{
				finalPayload: []byte(`{"error":{"type":"model_cooldown","message":"current Home retry window","retryable":true,"retry_after_ms":10000,"request_retry":1}}`),
			}
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
			manager.SetRetryConfig(1, 20*time.Second, 0)
			manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
			manager.RegisterExecutor(executor)

			errExecute := path.invoke(manager)
			if !errors.Is(errExecute, upstreamErr) {
				t.Fatalf("execution error = %v, want earlier upstream error as cause", errExecute)
			}
			if errors.Is(errExecute, internalErr) {
				t.Fatalf("execution error = %v, current internal failure remained the cause", errExecute)
			}
			if !isHomeRetryRoundExhausted(errExecute) {
				t.Fatalf("execution error = %T %v, want current Home retry-round wrapper", errExecute, errExecute)
			}
			if retryAfter := retryAfterFromError(errExecute); retryAfter == nil || *retryAfter != 10*time.Second {
				t.Fatalf("retry after = %v, want current Home delay 10s", retryAfter)
			}
			if got := SafeResponseHeaders(errExecute).Get("Retry-After"); got != "10" {
				t.Fatalf("safe Retry-After header = %q, want 10", got)
			}
		})
	}
}
