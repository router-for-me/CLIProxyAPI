package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ttftProbeExecutor records whether the attempt context was already canceled
// when ExecuteStream was entered, then returns an immediately closed stream.
type ttftProbeExecutor struct {
	calls         atomic.Int32
	ctxErrAtEntry error
}

func (e *ttftProbeExecutor) Identifier() string { return "gemini" }

func (e *ttftProbeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftProbeExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	e.ctxErrAtEntry = ctx.Err()
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *ttftProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftProbeExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }

func (e *ttftProbeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestStreamTTFTTimerArmedAfterInterception is a regression guard for the
// codex P2 finding on PR #4881: the first-chunk timeout timer used to be
// armed before applyRequestAfterAuthInterceptor, so a slow interceptor could
// burn the whole TTFT budget and ExecuteStream would be invoked with an
// already-canceled context, producing a retryable 504 that cooled the
// credential although no upstream request was ever attempted. The timer must
// only be armed after local interception and request preparation complete.
func TestStreamTTFTTimerArmedAfterInterception(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-ttft", Provider: "gemini", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	exec := &ttftProbeExecutor{}
	manager.RegisterExecutor(exec)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{"stream_first_chunk_timeout_ms": 50},
		RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			// Deliberately slower than the 50ms TTFT budget: pre-fix the timer
			// fired during this sleep and canceled the attempt context.
			time.Sleep(200 * time.Millisecond)
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}
	_, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, opts)

	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("expected executor to be invoked once, got %d", got)
	}
	if exec.ctxErrAtEntry != nil {
		t.Fatalf("attempt context was already canceled at ExecuteStream entry: %v", exec.ctxErrAtEntry)
	}
	if err != nil && statusCodeFromError(err) == http.StatusGatewayTimeout {
		t.Fatalf("TTFT timeout fired before any upstream request was attempted: %v", err)
	}
}
