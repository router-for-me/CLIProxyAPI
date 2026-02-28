package handlers

import (
	"context"
	"net/http"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type failOnceStreamExecutor struct {
	mu    sync.Mutex
	calls int
}

func (e *failOnceStreamExecutor) Identifier() string { return "codex" }

func (e *failOnceStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *failOnceStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()

	ch := make(chan coreexecutor.StreamChunk, 1)
	if call == 1 {
		ch <- coreexecutor.StreamChunk{
			Err: &coreauth.Error{
				Code:       "unauthorized",
				Message:    "unauthorized",
				Retryable:  false,
				HTTPStatus: http.StatusUnauthorized,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{
			Headers: http.Header{"X-Upstream-Attempt": {"1"}},
			Chunks:  ch,
		}, nil
	}

	ch <- coreexecutor.StreamChunk{Payload: []byte("ok")}
	close(ch)
	return &coreexecutor.StreamResult{
		Headers: http.Header{"X-Upstream-Attempt": {"2"}},
		Chunks:  ch,
	}, nil
}

func (e *failOnceStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *failOnceStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *failOnceStreamExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *failOnceStreamExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type payloadThenErrorStreamExecutor struct {
	mu    sync.Mutex
	calls int
}

func (e *payloadThenErrorStreamExecutor) Identifier() string { return "codex" }

func (e *payloadThenErrorStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *payloadThenErrorStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()

	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte("partial")}
	ch <- coreexecutor.StreamChunk{
		Err: &coreauth.Error{
			Code:       "upstream_closed",
			Message:    "upstream closed",
			Retryable:  false,
			HTTPStatus: http.StatusBadGateway,
		},
	}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *payloadThenErrorStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *payloadThenErrorStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *payloadThenErrorStreamExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *payloadThenErrorStreamExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type authAwareStreamExecutor struct {
	mu      sync.Mutex
	calls   int
	authIDs []string
}

// initialFallbackPreludeChainExecutor simulates:
// 1) requested model: connection failure (fallback should start),
// 2) first fallback model: stream opens but fails before first payload byte,
// 3) second fallback model: stream succeeds.
type initialFallbackPreludeChainExecutor struct {
	mu     sync.Mutex
	models []string
}

type invalidJSONStreamExecutor struct{}

func (e *invalidJSONStreamExecutor) Identifier() string { return "codex" }

func (e *invalidJSONStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *invalidJSONStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\"")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *invalidJSONStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *invalidJSONStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *invalidJSONStreamExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *authAwareStreamExecutor) Identifier() string { return "codex" }

func (e *authAwareStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *authAwareStreamExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	_ = ctx
	_ = req
	_ = opts
	ch := make(chan coreexecutor.StreamChunk, 1)

	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	e.calls++
	e.authIDs = append(e.authIDs, authID)
	e.mu.Unlock()

	if authID == "auth1" {
		ch <- coreexecutor.StreamChunk{
			Err: &coreauth.Error{
				Code:       "unauthorized",
				Message:    "unauthorized",
				Retryable:  false,
				HTTPStatus: http.StatusUnauthorized,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}

	ch <- coreexecutor.StreamChunk{Payload: []byte("ok")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *authAwareStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *authAwareStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *authAwareStreamExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *authAwareStreamExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

func (e *authAwareStreamExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.authIDs))
	copy(out, e.authIDs)
	return out
}

func (e *initialFallbackPreludeChainExecutor) Identifier() string { return "codex" }

func (e *initialFallbackPreludeChainExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *initialFallbackPreludeChainExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	e.mu.Unlock()

	switch req.Model {
	case "test-model":
		// Initial connection failure should trigger fallback chain.
		return nil, &coreauth.Error{
			Code:       "model_not_found",
			Message:    "model_not_found",
			HTTPStatus: http.StatusNotFound,
		}
	case "test-model-fb1":
		// Connection succeeds, but prelude chunk fails.
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{
			Err: &coreauth.Error{
				Code:       "upstream_unavailable",
				Message:    "upstream unavailable",
				HTTPStatus: http.StatusServiceUnavailable,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{
			Headers: http.Header{"X-Upstream-Model": {"test-model-fb1"}},
			Chunks:  ch,
		}, nil
	case "test-model-fb2":
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{Payload: []byte("ok")}
		close(ch)
		return &coreexecutor.StreamResult{
			Headers: http.Header{"X-Upstream-Model": {"test-model-fb2"}},
			Chunks:  ch,
		}, nil
	default:
		return nil, &coreauth.Error{
			Code:       "unknown_model",
			Message:    "unknown model",
			HTTPStatus: http.StatusBadRequest,
		}
	}
}

func (e *initialFallbackPreludeChainExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *initialFallbackPreludeChainExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *initialFallbackPreludeChainExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *initialFallbackPreludeChainExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func TestExecuteStreamWithAuthManager_RetriesBeforeFirstByte(t *testing.T) {
	executor := &failOnceStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		PassthroughHeaders: true,
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}

	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}

	if string(got) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(got))
	}
	if executor.Calls() != 2 {
		t.Fatalf("expected 2 stream attempts, got %d", executor.Calls())
	}
	upstreamAttemptHeader := upstreamHeaders.Get("X-Upstream-Attempt")
	if upstreamAttemptHeader != "2" {
		t.Fatalf("expected upstream header from retry attempt, got %q", upstreamAttemptHeader)
	}
}

func TestExecuteStreamWithAuthManager_HeaderPassthroughDisabledByDefault(t *testing.T) {
	executor := &failOnceStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}

	if string(got) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(got))
	}
	if upstreamHeaders != nil {
		t.Fatalf("expected nil upstream headers when passthrough is disabled, got %#v", upstreamHeaders)
	}
}

func TestExecuteStreamWithAuthManager_DoesNotRetryAfterFirstByte(t *testing.T) {
	executor := &payloadThenErrorStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if errChan == nil {
		t.Fatalf("expected non-nil errChan")
	}
	if dataChan == nil {
		msg := <-errChan
		if msg == nil || msg.Error == nil {
			t.Fatalf("expected non-nil dataChan, got nil with empty error message")
		}
		t.Fatalf("expected non-nil dataChan, got error: status=%d err=%v", msg.StatusCode, msg.Error)
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}

	var gotErr error
	var gotStatus int
	for msg := range errChan {
		if msg != nil && msg.Error != nil {
			gotErr = msg.Error
			gotStatus = msg.StatusCode
		}
	}

	if string(got) != "partial" {
		t.Fatalf("expected payload partial, got %q", string(got))
	}
	if gotErr == nil {
		t.Fatalf("expected terminal error, got nil")
	}
	if gotStatus != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %d", http.StatusBadGateway, gotStatus)
	}
	if executor.Calls() != 1 {
		t.Fatalf("expected 1 stream attempt, got %d", executor.Calls())
	}
}

func TestExecuteStreamWithAuthManager_InitialFallbackPreludeContinuesChain(t *testing.T) {
	executor := &initialFallbackPreludeChainExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{
		{ID: "test-model"},
		{ID: "test-model-fb1"},
		{ID: "test-model-fb2"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
	})
	if providers := util.GetProviderName("test-model"); len(providers) == 0 {
		t.Fatalf("expected providers for test-model, got none")
	}
	if providers := util.GetProviderName("test-model-fb1"); len(providers) == 0 {
		t.Fatalf("expected providers for test-model-fb1, got none")
	}
	if providers := util.GetProviderName("test-model-fb2"); len(providers) == 0 {
		t.Fatalf("expected providers for test-model-fb2, got none")
	}

	rtCfg := &internalconfig.Config{
		ModelFallback: internalconfig.ModelFallback{
			Enabled: true,
			Rules: []internalconfig.ModelFallbackRule{
				{
					From: "test-model",
					To:   []string{"test-model-fb1", "test-model-fb2"},
				},
			},
		},
	}
	rtCfg.SanitizeModelFallback()
	manager.SetConfig(rtCfg)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		PassthroughHeaders: true,
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 0,
		},
	}, manager)

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}

	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}

	if string(got) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(got))
	}
	models := executor.Models()
	if len(models) != 3 {
		t.Fatalf("expected 3 stream attempts, got %d: %v", len(models), models)
	}
	expected := []string{"test-model", "test-model-fb1", "test-model-fb2"}
	for i := range expected {
		if models[i] != expected[i] {
			t.Fatalf("attempt[%d] = %q, want %q (all=%v)", i, models[i], expected[i], models)
		}
	}
	if v := upstreamHeaders.Get("X-Upstream-Model"); v != "test-model-fb2" {
		t.Fatalf("expected upstream headers from final fallback model, got %q", v)
	}
}

func TestExecuteStreamWithAuthManager_FallbackTriggerAfterRetriesDelaysSwitch(t *testing.T) {
	executor := &initialFallbackPreludeChainExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}
	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{
		{ID: "test-model"},
		{ID: "test-model-fb1"},
		{ID: "test-model-fb2"},
	})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{
		{ID: "test-model"},
		{ID: "test-model-fb1"},
		{ID: "test-model-fb2"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	triggerAfterRetries := 1
	rtCfg := &internalconfig.Config{
		ModelFallback: internalconfig.ModelFallback{
			Enabled:             true,
			TriggerAfterRetries: &triggerAfterRetries,
			Rules: []internalconfig.ModelFallbackRule{
				{
					From: "test-model",
					To:   []string{"test-model-fb1", "test-model-fb2"},
				},
			},
		},
	}
	rtCfg.SanitizeModelFallback()
	manager.SetConfig(rtCfg)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		PassthroughHeaders: true,
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 0,
		},
	}, manager)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if errChan == nil {
		t.Fatalf("expected non-nil errChan")
	}
	if dataChan == nil {
		msg := <-errChan
		if msg == nil || msg.Error == nil {
			t.Fatalf("expected non-nil dataChan, got nil with empty error message")
		}
		t.Fatalf("expected non-nil dataChan, got error: status=%d err=%v", msg.StatusCode, msg.Error)
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}
	if string(got) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(got))
	}

	// Expected sequence:
	// - test-model fails initial connect
	// - test-model retried once (trigger-after-retries=1)
	// - switch to fb1
	// - fb1 prelude failure retried once
	// - switch to fb2 success
	models := executor.Models()
	expected := []string{"test-model", "test-model", "test-model-fb1", "test-model-fb1", "test-model-fb2"}
	if len(models) != len(expected) {
		t.Fatalf("expected %d stream attempts, got %d: %v", len(expected), len(models), models)
	}
	for i := range expected {
		if models[i] != expected[i] {
			t.Fatalf("attempt[%d] = %q, want %q (all=%v)", i, models[i], expected[i], models)
		}
	}
}

func TestExecuteStreamWithAuthManager_PinnedAuthKeepsSameUpstream(t *testing.T) {
	executor := &authAwareStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	ctx := WithPinnedAuthID(context.Background(), "auth1")
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}

	var gotErr error
	for msg := range errChan {
		if msg != nil && msg.Error != nil {
			gotErr = msg.Error
		}
	}

	if len(got) != 0 {
		t.Fatalf("expected empty payload, got %q", string(got))
	}
	if gotErr == nil {
		t.Fatalf("expected terminal error, got nil")
	}
	authIDs := executor.AuthIDs()
	if len(authIDs) == 0 {
		t.Fatalf("expected at least one upstream attempt")
	}
	for _, authID := range authIDs {
		if authID != "auth1" {
			t.Fatalf("expected all attempts on auth1, got sequence %v", authIDs)
		}
	}
}

func TestExecuteStreamWithAuthManager_SelectedAuthCallbackReceivesAuthID(t *testing.T) {
	executor := &authAwareStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth2 := &coreauth.Auth{
		ID:       "auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test2@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("manager.Register(auth2): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 0,
		},
	}, manager)

	selectedAuthID := ""
	ctx := WithSelectedAuthIDCallback(context.Background(), func(authID string) {
		selectedAuthID = authID
	})
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}

	if string(got) != "ok" {
		t.Fatalf("expected payload ok, got %q", string(got))
	}
	if selectedAuthID != "auth2" {
		t.Fatalf("selectedAuthID = %q, want %q", selectedAuthID, "auth2")
	}
}

func TestExecuteStreamWithAuthManager_ValidatesOpenAIResponsesStreamDataJSON(t *testing.T) {
	executor := &invalidJSONStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "test1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai-response", "test-model", []byte(`{"model":"test-model"}`), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
	}

	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty payload, got %q", string(got))
	}

	gotErr := false
	for msg := range errChan {
		if msg == nil {
			continue
		}
		if msg.StatusCode != http.StatusBadGateway {
			t.Fatalf("expected status %d, got %d", http.StatusBadGateway, msg.StatusCode)
		}
		if msg.Error == nil {
			t.Fatalf("expected error")
		}
		gotErr = true
	}
	if !gotErr {
		t.Fatalf("expected terminal error")
	}
}
