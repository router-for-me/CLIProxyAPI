package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type repeatedHomeAuthDispatcher struct {
	calls atomic.Int32
}

func (d *repeatedHomeAuthDispatcher) HeartbeatOK() bool {
	return true
}

func (d *repeatedHomeAuthDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	d.calls.Add(1)
	raw, _ := json.Marshal(homeAuthDispatchResponse{
		Auth: Auth{
			ID:       "home-auth-1",
			Provider: "home-loop-test",
			Status:   StatusActive,
			Metadata: map[string]any{"email": "loop@example.com"},
		},
	})
	return raw, nil
}

type unauthorizedHomeExecutor struct {
	calls atomic.Int32
}

func (e *unauthorizedHomeExecutor) Identifier() string { return "home-loop-test" }

func (e *unauthorizedHomeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func (e *unauthorizedHomeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing access token"}
}

func TestManagerExecuteHomeStopsWhenDispatchRepeatsTriedAuth(t *testing.T) {
	dispatcher := &repeatedHomeAuthDispatcher{}
	oldCurrentHomeDispatcher := currentHomeDispatcher
	currentHomeDispatcher = func() homeAuthDispatcher {
		return dispatcher
	}
	t.Cleanup(func() {
		currentHomeDispatcher = oldCurrentHomeDispatcher
	})

	executor := &unauthorizedHomeExecutor{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.RegisterExecutor(executor)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := manager.Execute(ctx, []string{"home-loop-test"}, cliproxyexecutor.Request{Model: "gemini-3.5-flash-low"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("Execute error = nil, want missing access token")
	}
	if statusCodeFromError(err) != http.StatusUnauthorized {
		t.Fatalf("Execute error status = %d, want 401 (%v)", statusCodeFromError(err), err)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1", got)
	}
	if got := dispatcher.calls.Load(); got != 2 {
		t.Fatalf("home dispatch calls = %d, want 2", got)
	}
}
