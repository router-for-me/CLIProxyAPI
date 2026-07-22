package handlers

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type bootstrapTimeoutExecutor struct {
	mu             sync.Mutex
	calls          int
	authIDs        []string
	stallFirst     bool
	firstDelay     time.Duration
	betweenPayload time.Duration
	canceled       chan struct{}
	cancelOnce     sync.Once
	streamCanceled chan struct{}
	streamOnce     sync.Once
}

func (e *bootstrapTimeoutExecutor) Identifier() string { return "codex" }

func (e *bootstrapTimeoutExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *bootstrapTimeoutExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.stallFirst && call == 1 {
		go func() {
			<-ctx.Done()
			e.cancelOnce.Do(func() {
				if e.canceled != nil {
					close(e.canceled)
				}
			})
			close(chunks)
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if e.streamCanceled != nil {
		go func() {
			<-ctx.Done()
			e.streamOnce.Do(func() { close(e.streamCanceled) })
		}()
	}

	go func() {
		defer close(chunks)
		if e.firstDelay > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(e.firstDelay):
			}
		}
		select {
		case <-ctx.Done():
			return
		case chunks <- coreexecutor.StreamChunk{Payload: []byte("first")}:
		}
		if e.betweenPayload <= 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(e.betweenPayload):
		}
		select {
		case <-ctx.Done():
		case chunks <- coreexecutor.StreamChunk{Payload: []byte("second")}:
		}
	}()
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *bootstrapTimeoutExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *bootstrapTimeoutExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *bootstrapTimeoutExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *bootstrapTimeoutExecutor) snapshot() (int, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, append([]string(nil), e.authIDs...)
}

func newBootstrapTimeoutHandler(t *testing.T, executor *bootstrapTimeoutExecutor, retries, timeoutSeconds int, authIDs ...string) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	for _, authID := range authIDs {
		auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
		authID := authID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{
		BootstrapRetries:        retries,
		BootstrapTimeoutSeconds: timeoutSeconds,
	}}, manager)
}

func collectBootstrapTimeoutResult(handler *BaseAPIHandler) ([]byte, int) {
	data, _, errs := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	var body []byte
	if data != nil {
		for chunk := range data {
			body = append(body, chunk...)
		}
	}
	status := 0
	for msg := range errs {
		if msg != nil {
			status = msg.StatusCode
		}
	}
	return body, status
}

func TestStreamingBootstrapTimeoutBounds(t *testing.T) {
	if got := StreamingBootstrapTimeout(nil); got != 0 {
		t.Fatalf("nil config timeout = %s, want 0", got)
	}
	if got := StreamingBootstrapTimeout(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapTimeoutSeconds: 20}}); got != 20*time.Second {
		t.Fatalf("timeout = %s, want 20s", got)
	}
	if got := StreamingBootstrapTimeout(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapTimeoutSeconds: 9999}}); got != 10*time.Minute {
		t.Fatalf("capped timeout = %s, want 10m", got)
	}
}

func TestStreamBootstrapTimeoutCancelsAndRetries(t *testing.T) {
	executor := &bootstrapTimeoutExecutor{stallFirst: true, canceled: make(chan struct{})}
	handler := newBootstrapTimeoutHandler(t, executor, 1, 1, "auth1", "auth2")
	start := time.Now()
	body, status := collectBootstrapTimeoutResult(handler)
	if status != 0 || string(body) != "first" {
		t.Fatalf("status=%d body=%q", status, body)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Fatalf("request returned before configured timeout")
	}
	select {
	case <-executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("timed-out attempt was not canceled")
	}
	calls, authIDs := executor.snapshot()
	if calls != 2 || len(authIDs) != 2 || authIDs[0] == authIDs[1] {
		t.Fatalf("calls=%d authIDs=%v, want retry on a different auth", calls, authIDs)
	}
}

func TestStreamBootstrapTimeoutReturnsGatewayTimeoutWithoutRetry(t *testing.T) {
	executor := &bootstrapTimeoutExecutor{stallFirst: true, canceled: make(chan struct{})}
	handler := newBootstrapTimeoutHandler(t, executor, 0, 1, "auth1")
	body, status := collectBootstrapTimeoutResult(handler)
	if len(body) != 0 || status != http.StatusGatewayTimeout {
		t.Fatalf("status=%d body=%q", status, body)
	}
}

func TestStreamBootstrapTimeoutDisarmsAfterFirstPayload(t *testing.T) {
	executor := &bootstrapTimeoutExecutor{firstDelay: 100 * time.Millisecond, betweenPayload: 1100 * time.Millisecond, streamCanceled: make(chan struct{})}
	handler := newBootstrapTimeoutHandler(t, executor, 0, 1, "auth1")
	body, status := collectBootstrapTimeoutResult(handler)
	if status != 0 || string(body) != "firstsecond" {
		t.Fatalf("status=%d body=%q", status, body)
	}
	if calls, _ := executor.snapshot(); calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
	select {
	case <-executor.streamCanceled:
	case <-time.After(time.Second):
		t.Fatal("bootstrap attempt context was not released after stream completion")
	}
}
