package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// inFlightBlockingExecutor blocks inside Execute until released so the test can
// observe the manager in-flight snapshot mid-request.
type inFlightBlockingExecutor struct {
	id        string
	release   chan struct{}
	entered   chan struct{}
	releaseMu sync.Once
}

func newInFlightBlockingExecutor(id string) *inFlightBlockingExecutor {
	return &inFlightBlockingExecutor{
		id:      id,
		release: make(chan struct{}),
		entered: make(chan struct{}, 4),
	}
}

func (e *inFlightBlockingExecutor) Identifier() string { return e.id }

func (e *inFlightBlockingExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.entered <- struct{}{}
	<-e.release
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"ok"}`)}, nil
}

func (e *inFlightBlockingExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not supported")
}

func (e *inFlightBlockingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *inFlightBlockingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *inFlightBlockingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

// inFlightStreamExecutor returns a stream whose chunks channel stays open until
// the test closes it, letting the stream lifetime be observed via the snapshot.
type inFlightStreamExecutor struct {
	id     string
	chunks chan cliproxyexecutor.StreamChunk
}

func (e *inFlightStreamExecutor) Identifier() string { return e.id }

func (e *inFlightStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *inFlightStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (e *inFlightStreamExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *inFlightStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *inFlightStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

func registerInFlightAuth(t *testing.T, m *Manager, authID, provider, model string) {
	t.Helper()
	auth := &Auth{ID: authID, Provider: provider, Status: StatusActive}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
}

func waitForInFlightCount(t *testing.T, m *Manager, want int) InFlightSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := m.InFlightSnapshot()
		if snapshot.InFlight == int64(want) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("in-flight count = %d, want %d (snapshot: %+v)", snapshot.InFlight, want, snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestManager_InFlightSnapshot_NonStreaming verifies that an in-flight
// non-streaming request is visible in the manager snapshot while it executes.
func TestManager_InFlightSnapshot_NonStreaming(t *testing.T) {
	executor := newInFlightBlockingExecutor("inflight-provider")
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	model := "inflight-model"
	registerInFlightAuth(t, m, "auth-inflight-1", executor.Identifier(), model)

	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}
	done := make(chan error, 1)
	go func() {
		_, errExec := m.Execute(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
		done <- errExec
	}()

	<-executor.entered
	snapshot := waitForInFlightCount(t, m, 1)
	if snapshot.ByProvider[executor.Identifier()] != 1 {
		t.Fatalf("by_provider[%q] = %d, want 1 (snapshot: %+v)", executor.Identifier(), snapshot.ByProvider[executor.Identifier()], snapshot)
	}
	if snapshot.OldestStartedAt.IsZero() {
		t.Fatal("expected oldest_started_at to be set while a request is in flight")
	}

	executor.releaseMu.Do(func() { close(executor.release) })
	if errExec := <-done; errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	waitForInFlightCount(t, m, 0)
}

// TestManager_InFlightSnapshot_Streaming verifies that a long-lived streaming
// request remains counted until the chunk channel closes.
func TestManager_InFlightSnapshot_Streaming(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	executor := &inFlightStreamExecutor{id: "inflight-stream-provider", chunks: chunks}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	model := "inflight-stream-model"
	registerInFlightAuth(t, m, "auth-inflight-stream-1", executor.Identifier(), model)

	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}
	type streamOutcome struct {
		result *cliproxyexecutor.StreamResult
		err    error
	}
	outcome := make(chan streamOutcome, 1)
	go func() {
		result, errStream := m.ExecuteStream(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
		outcome <- streamOutcome{result: result, err: errStream}
	}()
	// readStreamBootstrap blocks until the first payload chunk arrives.
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: bootstrap\n\n")}
	streamRes := <-outcome
	result, errStream := streamRes.result, streamRes.err
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	waitForInFlightCount(t, m, 1)

	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: hello\n\n")}
	if chunk, ok := <-result.Chunks; !ok || len(chunk.Payload) == 0 {
		t.Fatalf("expected forwarded chunk, got ok=%v payload=%q", ok, chunk.Payload)
	}
	if snapshot := m.InFlightSnapshot(); snapshot.InFlight != 1 {
		t.Fatalf("expected stream to remain in flight, snapshot: %+v", snapshot)
	}

	close(chunks)
	for range result.Chunks {
	}
	waitForInFlightCount(t, m, 0)
}

// TestManager_InFlightSnapshot_DisabledInHomeMode verifies that the standalone
// in-flight tracker stays empty when Home mode is enabled, since the execution
// registry covers that mode.
func TestManager_InFlightSnapshot_DisabledInHomeMode(t *testing.T) {
	executor := newInFlightBlockingExecutor("inflight-home-provider")
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetConfigSnapshot(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})

	snapshot := m.InFlightSnapshot()
	if snapshot.InFlight != 0 {
		t.Fatalf("expected empty snapshot in home mode, got %+v", snapshot)
	}
}

// inFlightRetryableStatusError is a retryable upstream error for retry tests.
type inFlightRetryableStatusError struct{}

func (inFlightRetryableStatusError) Error() string   { return "upstream unavailable" }
func (inFlightRetryableStatusError) StatusCode() int { return http.StatusServiceUnavailable }

// inFlightFailThenBlockExecutor fails the first Execute call with a retryable
// error and blocks in subsequent calls until released.
type inFlightFailThenBlockExecutor struct {
	id          string
	calls       atomic.Int32
	release     chan struct{}
	releaseOnce sync.Once
}

func newInFlightFailThenBlockExecutor(id string) *inFlightFailThenBlockExecutor {
	return &inFlightFailThenBlockExecutor{id: id, release: make(chan struct{})}
}

func (e *inFlightFailThenBlockExecutor) Identifier() string { return e.id }

func (e *inFlightFailThenBlockExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := e.calls.Add(1)
	if call == 1 {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return cliproxyexecutor.Response{}, inFlightRetryableStatusError{}
	}
	<-e.release
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"ok"}`)}, nil
}

func (e *inFlightFailThenBlockExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("stream not supported")
}

func (e *inFlightFailThenBlockExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *inFlightFailThenBlockExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not supported")
}

func (e *inFlightFailThenBlockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not supported")
}

// TestManager_InFlightSnapshot_CoversRetryWait verifies that a request waiting
// between retry rounds still appears in the snapshot instead of blinking to 0.
func TestManager_InFlightSnapshot_CoversRetryWait(t *testing.T) {
	executor := newInFlightFailThenBlockExecutor("inflight-retry-provider")
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)
	m.SetRetryConfig(1, 20*time.Second, 0)

	model := "inflight-retry-model"
	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-inflight-retry-1",
		Provider: executor.Identifier(),
		Metadata: map[string]any{"request_retry": 1, "disable_cooling": true},
	}); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient("auth-inflight-retry-1", executor.Identifier(), []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("auth-inflight-retry-1")
	})

	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}
	done := make(chan error, 1)
	go func() {
		_, errExec := m.Execute(context.Background(), []string{executor.Identifier()}, req, cliproxyexecutor.Options{})
		done <- errExec
	}()

	// Wait for the first call to record its retryable failure. The request then
	// enters the retry wait window while the second call blocks on release.
	deadline := time.Now().Add(5 * time.Second)
	for executor.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if executor.calls.Load() < 1 {
		t.Fatalf("expected the first upstream call to happen, calls = %d", executor.calls.Load())
	}
	// Allow the retry loop to record the failure and enter its wait or the
	// blocking second call; either way the request is still active and must
	// remain visible in the snapshot.
	deadline = time.Now().Add(2 * time.Second)
	for {
		snapshot := m.InFlightSnapshot()
		if snapshot.InFlight == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("in_flight = %d during retry, want 1 (snapshot: %+v)", snapshot.InFlight, snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}

	executor.releaseOnce.Do(func() { close(executor.release) })
	if errExec := <-done; errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	waitForInFlightCount(t, m, 0)
}

// TestManager_InFlightSnapshot_StreamCancel verifies that canceling the request
// context releases the in-flight scope even while the upstream chunk channel
// stays open and silent after the first chunk was already forwarded.
func TestManager_InFlightSnapshot_StreamCancel(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	executor := &inFlightStreamExecutor{id: "inflight-cancel-provider", chunks: chunks}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	model := "inflight-cancel-model"
	registerInFlightAuth(t, m, "auth-inflight-cancel-1", executor.Identifier(), model)

	ctx, cancel := context.WithCancel(context.Background())
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}

	type streamOutcome struct {
		result *cliproxyexecutor.StreamResult
		err    error
	}
	outcome := make(chan streamOutcome, 1)
	go func() {
		result, errStream := m.ExecuteStream(ctx, []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
		outcome <- streamOutcome{result: result, err: errStream}
	}()
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: bootstrap\n\n")}
	res := <-outcome
	if res.err != nil {
		t.Fatalf("ExecuteStream() error = %v", res.err)
	}
	result := res.result

	waitForInFlightCount(t, m, 1)

	// Consume the forwarded bootstrap chunk so the wrapper is blocked waiting
	// for the next upstream chunk (receive branch) when cancel happens.
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: second\n\n")}
	if chunk, ok := <-result.Chunks; !ok || len(chunk.Payload) == 0 {
		t.Fatalf("expected forwarded chunk, got ok=%v payload=%q", ok, chunk.Payload)
	}
	if snapshot := m.InFlightSnapshot(); snapshot.InFlight != 1 {
		t.Fatalf("expected stream to remain in flight, snapshot: %+v", snapshot)
	}

	// The upstream keeps its chunk channel open but silent; canceling the
	// request context must release the scope without needing another chunk.
	cancel()
	waitForInFlightCount(t, m, 0)

	// Close the source so the drain path and the output channel can finish
	// instead of leaking a background goroutine.
	close(chunks)
	for range result.Chunks {
	}
}

// TestManager_InFlightSnapshot_StreamCancelBeforeConsume verifies the send
// branch: cancellation while the first chunk is still queued for the caller
// also releases the in-flight scope.
func TestManager_InFlightSnapshot_StreamCancelBeforeConsume(t *testing.T) {
	chunks := make(chan cliproxyexecutor.StreamChunk)
	executor := &inFlightStreamExecutor{id: "inflight-cancel-early-provider", chunks: chunks}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(executor)

	model := "inflight-cancel-early-model"
	registerInFlightAuth(t, m, "auth-inflight-cancel-early-1", executor.Identifier(), model)

	ctx, cancel := context.WithCancel(context.Background())
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}

	type streamOutcome struct {
		result *cliproxyexecutor.StreamResult
		err    error
	}
	outcome := make(chan streamOutcome, 1)
	go func() {
		result, errStream := m.ExecuteStream(ctx, []string{executor.Identifier()}, req, cliproxyexecutor.Options{Stream: true})
		outcome <- streamOutcome{result: result, err: errStream}
	}()
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: bootstrap\n\n")}
	res := <-outcome
	if res.err != nil {
		t.Fatalf("ExecuteStream() error = %v", res.err)
	}
	result := res.result

	waitForInFlightCount(t, m, 1)

	// Cancel without consuming the queued bootstrap chunk; the send branch of
	// the wrapper must observe the cancellation and release the scope.
	cancel()
	waitForInFlightCount(t, m, 0)

	close(chunks)
	for range result.Chunks {
	}
}
