package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type timedStreamExecutor struct {
	id      string
	delay   time.Duration
	payload []byte
	err     error

	mu    sync.Mutex
	calls int
	seen  []string
}

func (e *timedStreamExecutor) Identifier() string { return e.id }

func (e *timedStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *timedStreamExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = req
	_ = opts
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.calls++
	e.seen = append(e.seen, authID)
	e.mu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(e.delay):
	}
	if e.err != nil {
		return nil, e.err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: e.payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *timedStreamExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *timedStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *timedStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *timedStreamExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type startupChunkErrorExecutor struct {
	mu      sync.Mutex
	calls   int
	authIDs []string
	badAuth string
	status  int
	code    string
	message string
}

func (e *startupChunkErrorExecutor) Identifier() string { return "codex" }

func (e *startupChunkErrorExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *startupChunkErrorExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = req
	_ = opts
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	e.mu.Lock()
	e.calls++
	e.authIDs = append(e.authIDs, authID)
	e.mu.Unlock()

	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	if authID == e.badAuth {
		chunks <- cliproxyexecutor.StreamChunk{Err: &Error{Code: e.code, Message: e.message, HTTPStatus: e.status}}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *startupChunkErrorExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *startupChunkErrorExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *startupChunkErrorExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func TestExecuteStream_HedgesMixedProvidersBeforeSlowFailure(t *testing.T) {
	t.Parallel()

	slowExecutor := &timedStreamExecutor{
		id:    "slow",
		delay: 250 * time.Millisecond,
		err: &Error{
			Code:       "unauthorized",
			Message:    "unauthorized",
			HTTPStatus: http.StatusUnauthorized,
		},
	}
	fastExecutor := &timedStreamExecutor{
		id:      "fast",
		delay:   20 * time.Millisecond,
		payload: []byte("ok"),
	}

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(slowExecutor)
	manager.RegisterExecutor(fastExecutor)

	slowAuth := &Auth{ID: "slow-auth", Provider: "slow", Status: StatusActive}
	fastAuth := &Auth{ID: "fast-auth", Provider: "fast", Status: StatusActive}
	if _, err := manager.Register(context.Background(), slowAuth); err != nil {
		t.Fatalf("manager.Register(slowAuth): %v", err)
	}
	if _, err := manager.Register(context.Background(), fastAuth); err != nil {
		t.Fatalf("manager.Register(fastAuth): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(slowAuth.ID, slowAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(fastAuth.ID, fastAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(slowAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(fastAuth.ID)
	})

	started := time.Now()
	streamResult, err := manager.ExecuteStream(context.Background(), []string{"slow", "fast"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if streamResult == nil {
		t.Fatal("ExecuteStream() streamResult = nil")
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}

	if string(payload) != "ok" {
		t.Fatalf("payload = %q, want %q", string(payload), "ok")
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("ExecuteStream() took %v, want under %v", elapsed, 150*time.Millisecond)
	}
	if slowExecutor.Calls() != 1 || fastExecutor.Calls() != 1 {
		t.Fatalf("expected one attempt per provider, got slow=%d fast=%d", slowExecutor.Calls(), fastExecutor.Calls())
	}
}

func TestExecuteStream_StartupChunkErrorMarksFailedAuthBeforeFallback(t *testing.T) {
	t.Parallel()

	executor := &startupChunkErrorExecutor{badAuth: "bad-auth", status: http.StatusUnauthorized, code: "unauthorized", message: "unauthorized"}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor)

	badAuth := &Auth{ID: "bad-auth", Provider: "codex", Status: StatusActive}
	goodAuth := &Auth{ID: "good-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), badAuth); err != nil {
		t.Fatalf("manager.Register(badAuth): %v", err)
	}
	if _, err := manager.Register(context.Background(), goodAuth); err != nil {
		t.Fatalf("manager.Register(goodAuth): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(badAuth.ID, badAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(goodAuth.ID, goodAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(badAuth.ID)
		registry.GetGlobalRegistry().UnregisterClient(goodAuth.ID)
	})

	streamResult, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if streamResult == nil {
		t.Fatal("ExecuteStream() streamResult = nil")
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "ok" {
		t.Fatalf("payload = %q, want %q", string(payload), "ok")
	}

	failedAuth, ok := manager.GetByID(badAuth.ID)
	if !ok {
		t.Fatalf("GetByID(%q) not found", badAuth.ID)
	}
	if !failedAuth.Unavailable {
		t.Fatalf("bad auth Unavailable = false, want true")
	}
	if failedAuth.Status != StatusError {
		t.Fatalf("bad auth Status = %q, want %q", failedAuth.Status, StatusError)
	}
	if failedAuth.LastError == nil || failedAuth.LastError.Code != "unauthorized" {
		t.Fatalf("bad auth LastError = %+v, want unauthorized", failedAuth.LastError)
	}
}

func TestExecuteStream_StartupChunkErrorMarksFailedAuthOnTerminalFailure(t *testing.T) {
	t.Parallel()

	executor := &startupChunkErrorExecutor{badAuth: "bad-auth", status: http.StatusUnauthorized, code: "unauthorized", message: "unauthorized"}
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor)

	badAuth := &Auth{ID: "bad-auth", Provider: "codex", Status: StatusActive}
	if _, err := manager.Register(context.Background(), badAuth); err != nil {
		t.Fatalf("manager.Register(badAuth): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(badAuth.ID, badAuth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(badAuth.ID)
	})

	streamResult, err := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{Stream: true})
	if err == nil {
		t.Fatalf("ExecuteStream() error = nil, want unauthorized")
	}
	if streamResult != nil {
		t.Fatalf("ExecuteStream() streamResult = %#v, want nil", streamResult)
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error status = %v, want %d", err, http.StatusUnauthorized)
	}

	failedAuth, ok := manager.GetByID(badAuth.ID)
	if !ok {
		t.Fatalf("GetByID(%q) not found", badAuth.ID)
	}
	if !failedAuth.Unavailable {
		t.Fatalf("bad auth Unavailable = false, want true")
	}
	if failedAuth.Status != StatusError {
		t.Fatalf("bad auth Status = %q, want %q", failedAuth.Status, StatusError)
	}
	if failedAuth.LastError == nil || failedAuth.LastError.Code != "unauthorized" {
		t.Fatalf("bad auth LastError = %+v, want unauthorized", failedAuth.LastError)
	}
}
