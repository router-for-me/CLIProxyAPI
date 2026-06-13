package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type flakyEOFExecutor struct {
	id string
	mu sync.Mutex

	calls int
}

func (e *flakyEOFExecutor) Identifier() string { return e.id }

func (e *flakyEOFExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls < 3 {
		return cliproxyexecutor.Response{}, errors.New(`Post "https://chatgpt.com/backend-api/codex/responses": EOF`)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *flakyEOFExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *flakyEOFExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *flakyEOFExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *flakyEOFExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *flakyEOFExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func TestManagerExecute_RetriesTransientEOFTransportErrors(t *testing.T) {
	const (
		provider = "codex"
		model    = "gpt-5.5"
	)

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(3, 30, 0)
	executor := &flakyEOFExecutor{id: provider}
	manager.RegisterExecutor(executor)

	auth := &Auth{ID: "auth-eof", Provider: provider, Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)

	resp, err := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want success after retries", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", string(resp.Payload))
	}
	if got := executor.Calls(); got != 3 {
		t.Fatalf("executor calls = %d, want 3", got)
	}
}
