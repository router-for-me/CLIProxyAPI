package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

// ttftRefreshProbeExecutor returns a retryable 401 on the first
// ExecuteStream, simulates a slow credential refresh, and records the attempt
// context state when the refreshed request is executed.
type ttftRefreshProbeExecutor struct {
	calls        atomic.Int32
	refreshCalls atomic.Int32
	retryCtxErr  error
}

func (e *ttftRefreshProbeExecutor) Identifier() string { return "gemini" }

func (e *ttftRefreshProbeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftRefreshProbeExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.calls.Add(1) == 1 {
		return nil, errors.New("upstream returned status 401")
	}
	e.retryCtxErr = ctx.Err()
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: hello\n\n")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *ttftRefreshProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *ttftRefreshProbeExecutor) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	select {
	case <-time.After(200 * time.Millisecond):
		return a, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *ttftRefreshProbeExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestStreamTTFTTimerRestartedAfterUnauthorizedRefresh mirrors the CPA
// regression guard for the codex P2 finding on PR #4881. The CPAPlus
// conductor already restarts the TTFT scope on a fresh attempt context for
// the unauthorized-refresh retry; this test locks that behavior in: a refresh
// slower than the first-chunk budget must not leave the retried ExecuteStream
// with an already-canceled context.
func TestStreamTTFTTimerRestartedAfterUnauthorizedRefresh(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-oauth",
		Provider: "gemini",
		Status:   StatusActive,
		Metadata: map[string]any{"auth_kind": "oauth", "refresh_token": "x"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	exec := &ttftRefreshProbeExecutor{}
	manager.RegisterExecutor(exec)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{"stream_first_chunk_timeout_ms": 50},
	}
	_, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, opts)

	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want success after refresh retry", err)
	}
	if got := exec.refreshCalls.Load(); got != 1 {
		t.Fatalf("expected one refresh, got %d", got)
	}
	if got := exec.calls.Load(); got != 2 {
		t.Fatalf("expected two ExecuteStream calls (401 then refreshed retry), got %d", got)
	}
	if exec.retryCtxErr != nil {
		t.Fatalf("refreshed attempt context was already canceled at ExecuteStream entry: %v", exec.retryCtxErr)
	}
}

// zeroPayloadStreamExecutor returns a stream whose only chunk carries no
// payload bytes, then closes it.
type zeroPayloadStreamExecutor struct {
	calls atomic.Int32
}

func (e *zeroPayloadStreamExecutor) Identifier() string { return "gemini" }

func (e *zeroPayloadStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *zeroPayloadStreamExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: nil}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *zeroPayloadStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *zeroPayloadStreamExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }

func (e *zeroPayloadStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestStreamZeroPayloadChunksAreEmptyCompletion is a regression guard for the
// codex P2 finding on PR #4881: emptiness was decided by chunk count, so a
// stream of only zero-payload chunks (dropped downstream by wrapStreamResult)
// was accepted as successful and the client received an empty completion
// without failover. Emptiness must be determined by buffered payload bytes.
// At the manager level a terminal bootstrap failure is delivered as an
// in-stream error chunk (streamErrorResult) with a nil Go error.
func TestStreamZeroPayloadChunksAreEmptyCompletion(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-zero-payload", Provider: "gemini", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	exec := &zeroPayloadStreamExecutor{}
	manager.RegisterExecutor(exec)

	result, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want in-stream error delivery", err)
	}
	if result == nil || result.Chunks == nil {
		t.Fatal("ExecuteStream() result has no chunk source")
	}
	payloadBytes := 0
	var streamErr error
	for chunk := range result.Chunks {
		payloadBytes += len(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if payloadBytes != 0 {
		t.Fatalf("stream delivered %d payload bytes, want 0", payloadBytes)
	}
	if streamErr == nil {
		t.Fatal("stream closed without an error chunk, want empty_stream (silent empty completion)")
	}
	if !strings.Contains(streamErr.Error(), "empty_stream") && !strings.Contains(streamErr.Error(), "empty completion") && !strings.Contains(streamErr.Error(), "closed before first payload") {
		t.Fatalf("stream error = %v, want an empty-stream error", streamErr)
	}
	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("expected one ExecuteStream call, got %d", got)
	}
}

type slowFirstChunkStreamExecutor struct {
	calls atomic.Int32
}

func (e *slowFirstChunkStreamExecutor) Identifier() string { return "gemini" }

func (e *slowFirstChunkStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *slowFirstChunkStreamExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}]}\n\n")}
		close(chunks)
	}()
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *slowFirstChunkStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *slowFirstChunkStreamExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return a, nil
}

func (e *slowFirstChunkStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestStreamTTFTDeadlineStoppedOnceUpstreamConnects(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-ttft-connected", Provider: "gemini", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	exec := &slowFirstChunkStreamExecutor{}
	manager.RegisterExecutor(exec)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{"stream_first_chunk_timeout_ms": 30},
	}
	result, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want established stream to not time out during chunk wait", err)
	}
	if result == nil || result.Chunks == nil {
		t.Fatal("ExecuteStream() returned nil result or nil chunks")
	}
	var payloadBytes int
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payloadBytes += len(chunk.Payload)
	}
	if payloadBytes == 0 {
		t.Fatal("expected non-empty stream payload")
	}
	if got := exec.calls.Load(); got != 1 {
		t.Fatalf("expected 1 ExecuteStream call, got %d", got)
	}
}

type errorStreamExecutor struct {
	calls atomic.Int32
}

func (e *errorStreamExecutor) Identifier() string { return "gemini" }

func (e *errorStreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *errorStreamExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, &Error{Code: "upstream_error", Message: "service unavailable", HTTPStatus: 503}
}

func (e *errorStreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *errorStreamExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return a, nil
}

func (e *errorStreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestStreamTTFTDeadlineStoppedOnExecutorError(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-ttft-error", Provider: "gemini", Status: StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "gemini", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	exec := &errorStreamExecutor{}
	manager.RegisterExecutor(exec)

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{"stream_first_chunk_timeout_ms": 20},
	}
	_, err := manager.ExecuteStream(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "test-model"}, opts)
	if err == nil {
		t.Fatal("expected error from executor, got nil")
	}
	if strings.Contains(err.Error(), "stream_first_chunk_timeout") || statusCodeFromError(err) == http.StatusGatewayTimeout {
		t.Fatalf("expected executor error, got TTFT timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream_error") && !strings.Contains(err.Error(), "service unavailable") {
		t.Fatalf("expected upstream_error, got: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
}
