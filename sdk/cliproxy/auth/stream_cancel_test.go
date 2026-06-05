package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type cancellableStreamExecutor struct {
	cancelled chan struct{}
	calls     atomic.Int32
	once      sync.Once
}

func newCancellableStreamExecutor() *cancellableStreamExecutor {
	return &cancellableStreamExecutor{cancelled: make(chan struct{})}
}

func (e *cancellableStreamExecutor) Identifier() string { return "codex" }

func (e *cancellableStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *cancellableStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	return &cliproxyexecutor.StreamResult{
		Chunks: ch,
		Cancel: func() {
			e.calls.Add(1)
			e.once.Do(func() { close(e.cancelled) })
		},
	}, nil
}

func (e *cancellableStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *cancellableStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *cancellableStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerExecuteStreamCancelsUpstreamWhenContextEnds(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	executor := newCancellableStreamExecutor()
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "cancel-stream-auth",
		Provider: "codex",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	ctx, cancel := context.WithCancel(context.Background())
	streamResult, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "model"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream: %v", errExecute)
	}

	select {
	case chunk := <-streamResult.Chunks:
		if string(chunk.Payload) != "first" {
			t.Fatalf("first payload = %q, want first", string(chunk.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}

	cancel()

	select {
	case <-executor.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected upstream stream cancel callback to run")
	}
	if calls := executor.calls.Load(); calls != 1 {
		t.Fatalf("cancel callback calls = %d, want 1", calls)
	}
}
