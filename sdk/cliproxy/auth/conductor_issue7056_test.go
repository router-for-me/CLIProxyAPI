package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type captureResultHook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
	done    chan Result
}

func (h *captureResultHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
	if h.done != nil {
		select {
		case h.done <- result:
		default:
		}
	}
}

func TestWrapStreamResultDoesNotReadReusedGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hook := &captureResultHook{done: make(chan Result, 1)}
	manager := NewManager(nil, nil, hook)
	const model = "gpt-5.4"
	auth := &Auth{
		ID:       "codex-siwc-stream",
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "outer-token",
			"use_siwc":     true,
			"siwc": map[string]any{
				"access_token": "siwc-token",
			},
		},
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	remaining := make(chan cliproxyexecutor.StreamChunk)
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\"}\n\n")}
			go func() {
				defer close(chunks)
				for chunk := range remaining {
					chunks <- chunk
				}
			}()
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req1.Header.Set("User-Agent", "custom-client/1.0")
	ginCtx.Request = req1
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	opts := cliproxyexecutor.Options{
		Stream:  true,
		Headers: req1.Header.Clone(),
	}

	streamResult, errStream := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	firstChunk, ok := <-streamResult.Chunks
	if !ok || len(firstChunk.Payload) == 0 {
		t.Fatalf("expected bootstrap chunk, got ok=%v chunk=%+v", ok, firstChunk)
	}

	// Simulate Gin reusing gin.Context for a subsequent Codex client request
	// concurrently with stream completion after the first handler returns.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req2.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
		ginCtx.Request = req2
	}()
	close(remaining)
	for range streamResult.Chunks {
	}
	wg.Wait()

	recorded := <-hook.done
	if recorded.AccessToken != "siwc-token" {
		t.Fatalf("recorded.AccessToken = %q, want %q (must not read reused gin.Context request headers)", recorded.AccessToken, "siwc-token")
	}
}

func TestWrapStreamResultEmptyTokenDoesNotReadReusedGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		authID = "codex-siwc-empty-stream-auth"
		model  = "gpt-5.4"
	)

	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	hook := &captureResultHook{done: make(chan Result, 1)}
	manager := NewManager(nil, nil, hook)
	if _, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"use_siwc": true,
		},
	}); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	remaining := make(chan cliproxyexecutor.StreamChunk)
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"id\":\"1\"}\n\n")}
			go func() {
				defer close(chunks)
				for chunk := range remaining {
					chunks <- chunk
				}
			}()
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	req1 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req1.Header.Set("User-Agent", "custom-client/1.0")
	ginCtx.Request = req1
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	opts := cliproxyexecutor.Options{
		Stream:  true,
		Headers: req1.Header.Clone(),
	}

	streamResult, errStream := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	firstChunk, ok := <-streamResult.Chunks
	if !ok || len(firstChunk.Payload) == 0 {
		t.Fatalf("expected bootstrap chunk, got ok=%v chunk=%+v", ok, firstChunk)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req2 := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		req2.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
		ginCtx.Request = req2
	}()
	close(remaining)
	for range streamResult.Chunks {
	}
	wg.Wait()

	recorded := <-hook.done
	if recorded.AccessToken != "" {
		t.Fatalf("recorded.AccessToken = %q, want empty", recorded.AccessToken)
	}
}
