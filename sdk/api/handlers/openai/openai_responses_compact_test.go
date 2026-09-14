package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type compactCaptureExecutor struct {
	alt          string
	sourceFormat string
	calls        int
}

func (e *compactCaptureExecutor) Identifier() string { return "test-provider" }

func (e *compactCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.alt = opts.Alt
	e.sourceFormat = opts.SourceFormat.String()
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *compactCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *compactCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *compactCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *compactCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesCompactRejectsStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusBadRequest)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls = %d, want 0", executor.calls)
	}
}

func TestOpenAIResponsesCompactExecute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusOK)
	}
	if executor.alt != "responses/compact" {
		t.Fatalf("alt = %q, want %q", executor.alt, "responses/compact")
	}
	if executor.sourceFormat != "openai-response" {
		t.Fatalf("source format = %q, want %q", executor.sourceFormat, "openai-response")
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

func TestOpenAIResponsesCompactDecodesZstdRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth3", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)

	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, errWrite := encoder.Write([]byte(`{"model":"test-model","input":"hello"}`)); errWrite != nil {
		t.Fatalf("zstd write: %v", errWrite)
	}
	if errClose := encoder.Close(); errClose != nil {
		t.Fatalf("zstd close: %v", errClose)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(compressed.Bytes()))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if executor.alt != "responses/compact" {
		t.Fatalf("alt = %q, want %q", executor.alt, "responses/compact")
	}
	if strings.TrimSpace(resp.Body.String()) != `{"ok":true}` {
		t.Fatalf("body = %s", resp.Body.String())
	}
}

type compactMockStatusError struct {
	code int
	msg  string
}

func (e compactMockStatusError) Error() string   { return e.msg }
func (e compactMockStatusError) StatusCode() int { return e.code }

type compactFailureMockExecutor struct {
	compactErr error
	normalResp []byte
	calls      int
	lastAlt    string
	lastAuthID string
}

func (e *compactFailureMockExecutor) Identifier() string { return "test-compact-provider" }

func (e *compactFailureMockExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.lastAlt = opts.Alt
	if auth != nil {
		e.lastAuthID = auth.ID
	}
	if opts.Alt == "responses/compact" {
		if e.compactErr != nil {
			return coreexecutor.Response{}, e.compactErr
		}
	}
	respPayload := e.normalResp
	if len(respPayload) == 0 {
		respPayload = []byte(`{"id":"resp_123","object":"response","status":"completed"}`)
	}
	return coreexecutor.Response{Payload: respPayload}, nil
}

func (e *compactFailureMockExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *compactFailureMockExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *compactFailureMockExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *compactFailureMockExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesCompactTransientFailureDoesNotCooldownAuthAndPreservesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactFailureMockExecutor{
		compactErr: compactMockStatusError{
			code: http.StatusInternalServerError,
			msg:  `{"error":{"message":"compact upstream temporary error","type":"api_error"}}`,
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	auth2 := &coreauth.Auth{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("Register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("Register auth2: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)
	router.POST("/v1/responses", h.Responses)

	// Send compact request which fails upstream on all auths with 500
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	// 1. Should return upstream status 500 and upstream error message (not generic 503 Service temporarily unavailable)
	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("compact status = %d, want %d; body = %s", resp.Code, http.StatusInternalServerError, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "compact upstream temporary error") {
		t.Fatalf("compact body = %s, want containing 'compact upstream temporary error'", resp.Body.String())
	}

	// 2. Auth model states should NOT be marked unavailable for normal traffic
	for _, authID := range []string{"auth1", "auth2"} {
		a, ok := manager.GetByID(authID)
		if !ok {
			t.Fatalf("auth %s not found", authID)
		}
		if state, exists := a.ModelStates["test-model"]; exists && state != nil {
			if state.Unavailable {
				t.Fatalf("auth %s model state marked Unavailable after compact failure", authID)
			}
			if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(time.Now()) {
				t.Fatalf("auth %s model state has NextRetryAfter %v in future", authID, state.NextRetryAfter)
			}
		}
	}

	// 3. Normal /v1/responses request should succeed immediately without auth cooldown errors
	reqNormal := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	reqNormal.Header.Set("Content-Type", "application/json")
	respNormal := httptest.NewRecorder()
	router.ServeHTTP(respNormal, reqNormal)

	if respNormal.Code != http.StatusOK {
		t.Fatalf("normal responses status = %d, want %d; body = %s", respNormal.Code, http.StatusOK, respNormal.Body.String())
	}
}

func TestOpenAIResponsesCompactRequestFaultStopsFallbackAndPreservesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &compactFailureMockExecutor{
		compactErr: compactMockStatusError{
			code: http.StatusNotFound,
			msg:  `404 page not found`,
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth1 := &coreauth.Auth{ID: "auth1", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	auth2 := &coreauth.Auth{ID: "auth2", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth1); err != nil {
		t.Fatalf("Register auth1: %v", err)
	}
	if _, err := manager.Register(context.Background(), auth2); err != nil {
		t.Fatalf("Register auth2: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth1.ID)
		registry.GetGlobalRegistry().UnregisterClient(auth2.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses/compact", h.Compact)
	router.POST("/v1/responses", h.Responses)

	// Send compact request which fails upstream with 404 (endpoint not supported / invalid)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	// 1. Should return upstream status 404 and upstream error message
	if resp.Code != http.StatusNotFound {
		t.Fatalf("compact status = %d, want %d; body = %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "404 page not found") {
		t.Fatalf("compact body = %s, want containing '404 page not found'", resp.Body.String())
	}

	// 2. Should stop fallback on request/capability fault (calls == 1)
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1 (fallback should stop)", executor.calls)
	}

	// 3. Auth model states should NOT be marked unavailable for normal traffic
	for _, authID := range []string{"auth1", "auth2"} {
		a, ok := manager.GetByID(authID)
		if !ok {
			t.Fatalf("auth %s not found", authID)
		}
		if state, exists := a.ModelStates["test-model"]; exists && state != nil {
			if state.Unavailable {
				t.Fatalf("auth %s model state marked Unavailable after compact failure", authID)
			}
		}
	}

	// 4. Normal /v1/responses request should succeed immediately
	reqNormal := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	reqNormal.Header.Set("Content-Type", "application/json")
	respNormal := httptest.NewRecorder()
	router.ServeHTTP(respNormal, reqNormal)

	if respNormal.Code != http.StatusOK {
		t.Fatalf("normal responses status = %d, want %d; body = %s", respNormal.Code, http.StatusOK, respNormal.Body.String())
	}
}
