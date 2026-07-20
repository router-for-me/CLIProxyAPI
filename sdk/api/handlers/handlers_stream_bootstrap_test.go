package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
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

type invalidJSONStreamExecutor struct{}

type splitResponsesEventStreamExecutor struct{}

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

func (e *splitResponsesEventStreamExecutor) Identifier() string { return "split-sse" }

func (e *splitResponsesEventStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *splitResponsesEventStreamExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed")}
	ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *splitResponsesEventStreamExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *splitResponsesEventStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *splitResponsesEventStreamExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
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
	if dataChan == nil || errChan == nil {
		t.Fatalf("expected non-nil channels")
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

func TestExecuteStreamWithAuthManager_EnrichesBootstrapRetryAuthUnavailableError(t *testing.T) {
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

	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 1,
		},
	}, manager)
	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte(`{"model":"test-model"}`), "")
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

	var gotErr *interfaces.ErrorMessage
	for msg := range errChan {
		if msg != nil {
			gotErr = msg
		}
	}
	if gotErr == nil {
		t.Fatalf("expected terminal error")
	}
	if gotErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", gotErr.StatusCode, http.StatusServiceUnavailable)
	}

	var authErr *coreauth.Error
	if !errors.As(gotErr.Error, &authErr) || authErr == nil {
		t.Fatalf("expected coreauth.Error, got %T", gotErr.Error)
	}
	if authErr.Code != "auth_unavailable" {
		t.Fatalf("code = %q, want %q", authErr.Code, "auth_unavailable")
	}
	if !strings.Contains(authErr.Message, "providers=codex") {
		t.Fatalf("message missing provider context: %q", authErr.Message)
	}
	if !strings.Contains(authErr.Message, "model=test-model") {
		t.Fatalf("message missing model context: %q", authErr.Message)
	}

	if executor.Calls() != 1 {
		t.Fatalf("expected exactly one upstream call before retry path selection failure, got %d", executor.Calls())
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

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	logging.SetGinRequestID(ginCtx, "1234abcd")

	selectedAuthID := ""
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	ctx = WithSelectedAuthIDCallback(ctx, func(authID string) {
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
	traceID := logging.GetGinCPATraceID(ginCtx)
	parts := strings.Split(traceID, "-")
	if len(parts) != 3 || parts[1] != auth2.Index || parts[2] != "1234abcd" {
		t.Fatalf("trace ID = %q, want timestamp-%s-1234abcd", traceID, auth2.Index)
	}
	if _, errParse := time.Parse("20060102150405", parts[0]); errParse != nil {
		t.Fatalf("trace timestamp = %q: %v", parts[0], errParse)
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

func TestExecuteStreamWithAuthManager_AllowsSplitOpenAIResponsesSSEEventLines(t *testing.T) {
	executor := &splitResponsesEventStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "auth1",
		Provider: "split-sse",
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

	var got []string
	for chunk := range dataChan {
		got = append(got, string(chunk))
	}

	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected error: %+v", msg)
		}
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 forwarded chunks, got %d: %#v", len(got), got)
	}
	if got[0] != "event: response.completed" {
		t.Fatalf("unexpected first chunk: %q", got[0])
	}
	expectedData := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}"
	if got[1] != expectedData {
		t.Fatalf("unexpected second chunk.\nGot:  %q\nWant: %q", got[1], expectedData)
	}
}

// slowRetryStreamExecutor fails its first stream attempt before any payload, then serves a
// stream whose first payload only arrives after delay. That reproduces the bootstrap-retry
// window: ExecuteStreamWithAuthManager has already returned on the error chunk while the
// SDK's background goroutine waits on a fresh, and equally silent, upstream.
type slowRetryStreamExecutor struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
}

func (e *slowRetryStreamExecutor) Identifier() string { return "codex" }

func (e *slowRetryStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *slowRetryStreamExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()

	if call == 1 {
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{
			Err: &coreauth.Error{
				Code:       "unauthorized",
				Message:    "unauthorized",
				HTTPStatus: http.StatusUnauthorized,
			},
		}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}

	ch := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(ch)
		timer := time.NewTimer(e.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case ch <- coreexecutor.StreamChunk{Payload: []byte("ok")}:
		case <-ctx.Done():
		}
	}()
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *slowRetryStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *slowRetryStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *slowRetryStreamExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{
		Code:       "not_implemented",
		Message:    "HttpRequest not implemented",
		HTTPStatus: http.StatusNotImplemented,
	}
}

func (e *slowRetryStreamExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// headerCountingRecorder counts WriteHeader calls so a test can prove the committed-error
// path never tries to write a second HTTP status.
type headerCountingRecorder struct {
	*httptest.ResponseRecorder
	writeHeaderCalls int
}

func (w *headerCountingRecorder) WriteHeader(code int) {
	w.writeHeaderCalls++
	w.ResponseRecorder.WriteHeader(code)
}

func newBootstrapStreamTestContext() (*gin.Context, *headerCountingRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := &headerCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	return c, recorder
}

// sseTestOptions wires StreamBootstrapOptions for a minimal SSE handler so each test only
// has to supply the Execute behaviour it exercises.
func sseTestOptions(c *gin.Context, interval time.Duration, uncommitted *int) StreamBootstrapOptions {
	return StreamBootstrapOptions{
		KeepAliveInterval: &interval,
		SetSSEHeaders: func() {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
		},
		WriteCommittedError: func(errMsg *interfaces.ErrorMessage) []byte {
			body := []byte("{\"error\":{\"message\":\"upstream failed\"}}")
			_, _ = c.Writer.Write([]byte("event: error\ndata: "))
			_, _ = c.Writer.Write(body)
			_, _ = c.Writer.Write([]byte("\n\n"))
			return body
		},
		WriteUncommittedError: func(*interfaces.ErrorMessage) {
			*uncommitted++
			c.Status(http.StatusInternalServerError)
			_, _ = c.Writer.Write([]byte("{\"error\":\"boom\"}"))
		},
		OnFirstChunk: func(headersCommitted bool, _ http.Header, chunk []byte) {
			if !headersCommitted {
				c.Header("Content-Type", "text/event-stream")
			}
			_, _ = c.Writer.Write([]byte("data: "))
			_, _ = c.Writer.Write(chunk)
			_, _ = c.Writer.Write([]byte("\n\n"))
		},
		Cancel: func(error) {},
	}
}

// (a) The first chunk arrives after the keep-alive interval: a heartbeat must precede it and
// the request must still finish as a normal 200 stream.
func TestBootstrapStream_HeartbeatsBeforeDelayedFirstChunk(t *testing.T) {
	c, recorder := newBootstrapStreamTestContext()
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	uncommitted := 0
	opts := sseTestOptions(c, 20*time.Millisecond, &uncommitted)
	opts.Execute = func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		time.Sleep(120 * time.Millisecond)
		data := make(chan []byte, 1)
		data <- []byte("hello")
		close(data)
		errs := make(chan *interfaces.ErrorMessage)
		close(errs)
		return data, http.Header{}, errs
	}

	handler.BootstrapStream(c, flusher, opts)

	body := recorder.Body.String()
	firstBeat := strings.Index(body, KeepAliveSSEComment)
	if firstBeat < 0 {
		t.Fatalf("expected a keep-alive heartbeat, got %q", body)
	}
	payload := strings.Index(body, "data: hello")
	if payload < 0 {
		t.Fatalf("expected the first chunk in the body, got %q", body)
	}
	if firstBeat > payload {
		t.Fatalf("expected the heartbeat before the first chunk, got %q", body)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if uncommitted != 0 {
		t.Fatalf("uncommitted error writer called %d times, want 0", uncommitted)
	}
}

// (b) An error that lands after a heartbeat already committed the headers must be reported
// in-band as an SSE error event, with exactly one WriteHeader and no lost request log.
func TestBootstrapStream_CommittedErrorBecomesSSEEvent(t *testing.T) {
	c, recorder := newBootstrapStreamTestContext()
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	uncommitted := 0
	opts := sseTestOptions(c, 20*time.Millisecond, &uncommitted)
	opts.Execute = func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		time.Sleep(120 * time.Millisecond)
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage, 1)
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("upstream failed"),
		}
		return data, http.Header{}, errs
	}

	handler.BootstrapStream(c, flusher, opts)

	body := recorder.Body.String()
	if !strings.Contains(body, KeepAliveSSEComment) {
		t.Fatalf("expected a keep-alive heartbeat, got %q", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected an SSE error event, got %q", body)
	}
	if uncommitted != 0 {
		t.Fatalf("uncommitted error writer called %d times, want 0 once headers are committed", uncommitted)
	}
	if recorder.writeHeaderCalls != 1 {
		t.Fatalf("WriteHeader called %d times, want exactly 1", recorder.writeHeaderCalls)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (the heartbeat already committed it)", recorder.Code, http.StatusOK)
	}
	logged, exists := c.Get("API_RESPONSE")
	if !exists {
		t.Fatal("expected the committed error to be appended to the request log")
	}
	loggedBytes, isBytes := logged.([]byte)
	if !isBytes || !strings.Contains(string(loggedBytes), "upstream failed") {
		t.Fatalf("request log = %v, want it to contain the error body", logged)
	}
}

// (c1) Once Execute has returned and the real channels are bound, the upstream can still go
// silent (a retry against a fresh upstream, a slow first frame after the bootstrap peek).
// The same ticker must keep covering that window instead of stopping at the hand-off, which
// is what a stop-the-goroutine-then-peek implementation gets wrong.
func TestBootstrapStream_HeartbeatsContinueAfterUpstreamBound(t *testing.T) {
	c, recorder := newBootstrapStreamTestContext()
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	uncommitted := 0
	opts := sseTestOptions(c, 20*time.Millisecond, &uncommitted)
	opts.Execute = func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		// Returns immediately, so every heartbeat below happens after the bind.
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)
		go func() {
			time.Sleep(200 * time.Millisecond)
			data <- []byte("late")
			close(data)
		}()
		return data, http.Header{}, errs
	}

	handler.BootstrapStream(c, flusher, opts)

	body := recorder.Body.String()
	payload := strings.Index(body, "data: late")
	if payload < 0 {
		t.Fatalf("expected the late chunk in the body, got %q", body)
	}
	beats := strings.Count(body[:payload], KeepAliveSSEComment)
	if beats < 2 {
		t.Fatalf("expected heartbeats to continue after the upstream was bound, got %d in %q", beats, body)
	}
	if uncommitted != 0 {
		t.Fatalf("uncommitted error writer called %d times, want 0", uncommitted)
	}
}

// (c2) End-to-end against the real SDK: the first upstream fails before any payload and the
// auth manager rotates to a slow second upstream. Note that this retry happens *inside*
// ExecuteStreamWithAuthManager, because coreauth's own readStreamBootstrap
// (sdk/cliproxy/auth/conductor.go:1896) peels off pre-payload errors before returning, so
// this covers the first-token window rather than the post-bind one that (c1) covers.
func TestBootstrapStream_HeartbeatsAcrossAuthRotationRetry(t *testing.T) {
	executor := &slowRetryStreamExecutor{delay: 200 * time.Millisecond}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{
		ID:       "retry-auth1",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "retry1@example.com"},
	}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("manager.Register(auth1): %v", err)
	}
	auth2 := &coreauth.Auth{
		ID:       "retry-auth2",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": "retry2@example.com"},
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

	c, recorder := newBootstrapStreamTestContext()
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 1},
	}, manager)
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("gin writer does not implement http.Flusher")
	}

	uncommitted := 0
	opts := sseTestOptions(c, 20*time.Millisecond, &uncommitted)
	opts.Execute = func() (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		return handler.ExecuteStreamWithAuthManager(context.Background(), "openai", "test-model", []byte("{\"model\":\"test-model\"}"), "")
	}

	handler.BootstrapStream(c, flusher, opts)

	body := recorder.Body.String()
	payload := strings.Index(body, "data: ok")
	if payload < 0 {
		t.Fatalf("expected the retried payload in the body, got %q", body)
	}
	beats := strings.Count(body[:payload], KeepAliveSSEComment)
	if beats < 2 {
		t.Fatalf("expected heartbeats across the auth-rotation retry, got %d in %q", beats, body)
	}
	if executor.Calls() != 2 {
		t.Fatalf("expected 2 stream attempts, got %d", executor.Calls())
	}
	if uncommitted != 0 {
		t.Fatalf("uncommitted error writer called %d times, want 0", uncommitted)
	}
}

func TestPendingStreamErrorUsesBufferedError(t *testing.T) {
	wantErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New("{\"error\":{\"message\":\"context too large\",\"type\":\"invalid_request_error\"}}"),
	}
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- wantErr
	close(errs)

	gotErr, ok := pendingStreamError(errs)
	if !ok {
		t.Fatal("expected pending stream error")
	}
	if gotErr != wantErr {
		t.Fatalf("pending error = %p, want %p", gotErr, wantErr)
	}
}
