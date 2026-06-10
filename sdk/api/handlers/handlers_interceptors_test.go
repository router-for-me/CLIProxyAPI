package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type handlerInterceptorTestHost struct {
	interceptRequestBeforeAuth func(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
	interceptRequestAfterAuth  func(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
	interceptResponse          func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse
	interceptStreamChunk       func(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse
}

type handlerInterceptorNoStreamTestHost struct {
	*handlerInterceptorTestHost
}

func (h *handlerInterceptorNoStreamTestHost) HasStreamInterceptors() bool {
	return false
}

func (h *handlerInterceptorTestHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if h != nil && h.interceptRequestBeforeAuth != nil {
		return h.interceptRequestBeforeAuth(ctx, req)
	}
	return pluginapi.RequestInterceptResponse{
		Headers: cloneHeader(req.Headers),
		Body:    cloneBytes(req.Body),
	}
}

func (h *handlerInterceptorTestHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if h != nil && h.interceptRequestAfterAuth != nil {
		return h.interceptRequestAfterAuth(ctx, req)
	}
	return pluginapi.RequestInterceptResponse{
		Headers: cloneHeader(req.Headers),
		Body:    cloneBytes(req.Body),
	}
}

func (h *handlerInterceptorTestHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	if h != nil && h.interceptResponse != nil {
		return h.interceptResponse(ctx, req)
	}
	return pluginapi.ResponseInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    cloneBytes(req.Body),
	}
}

func (h *handlerInterceptorTestHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	if h != nil && h.interceptStreamChunk != nil {
		return h.interceptStreamChunk(ctx, req)
	}
	return pluginapi.StreamChunkInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    cloneBytes(req.Body),
	}
}

type interceptorCaptureExecutor struct {
	provider string

	mu           sync.Mutex
	lastRequest  coreexecutor.Request
	lastOptions  coreexecutor.Options
	execute      func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	executeCount func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	stream       func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (e *interceptorCaptureExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return "codex"
}

func (e *interceptorCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return coreexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *interceptorCaptureExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.capture(req, opts)
	if e.stream != nil {
		return e.stream(ctx, auth, req, opts)
	}
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *interceptorCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *interceptorCaptureExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.capture(req, opts)
	if e.executeCount != nil {
		return e.executeCount(ctx, auth, req, opts)
	}
	return coreexecutor.Response{Payload: []byte("0")}, nil
}

func (e *interceptorCaptureExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *interceptorCaptureExecutor) capture(req coreexecutor.Request, opts coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRequest = coreexecutor.Request{
		Model:    req.Model,
		Payload:  cloneBytes(req.Payload),
		Format:   req.Format,
		Metadata: req.Metadata,
	}
	e.lastOptions = coreexecutor.Options{
		Stream:          opts.Stream,
		Alt:             opts.Alt,
		Headers:         cloneHeader(opts.Headers),
		Query:           opts.Query,
		OriginalRequest: cloneBytes(opts.OriginalRequest),
		SourceFormat:    opts.SourceFormat,
		Metadata:        opts.Metadata,
	}
}

func (e *interceptorCaptureExecutor) captured() (coreexecutor.Request, coreexecutor.Options) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastRequest, e.lastOptions
}

func newInterceptorHandler(t *testing.T, model string, executor *interceptorCaptureExecutor, cfg *sdkconfig.SDKConfig) *BaseAPIHandler {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "handler-interceptor-" + model,
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": model + "@example.com"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("manager.Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})
	return NewBaseAPIHandlers(cfg, manager)
}

func contextWithHeaders(headers http.Header) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	for key, values := range headers {
		for _, value := range values {
			c.Request.Header.Add(key, value)
		}
	}
	return context.WithValue(context.Background(), "gin", c)
}

func TestHandlerRequestInterceptorRewritesExecutorRequest(t *testing.T) {
	model := "handler-interceptor-request-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestBeforeAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			if req.SourceFormat != "openai" || req.Model != model || req.RequestedModel != model {
				t.Fatalf("unexpected request context: %#v", req)
			}
			if req.Headers.Get("X-Original") != "client" {
				t.Fatalf("request headers = %#v, want client header", req.Headers)
			}
			if req.Metadata == nil {
				t.Fatal("metadata = nil, want request metadata")
			}
			headers := cloneHeader(req.Headers)
			headers.Set("X-Original", "plugin")
			headers.Set("X-Plugin", "1")
			headers.Del("X-Remove")
			return pluginapi.RequestInterceptResponse{
				Headers: headers,
				Body:    []byte(fmt.Sprintf(`{"model":%q,"plugin":true}`, model)),
			}
		},
	})
	ctx := contextWithHeaders(http.Header{
		"X-Original": []string{"client"},
		"X-Remove":   []string{"yes"},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	gotReq, gotOpts := executor.captured()
	wantPayload := fmt.Sprintf(`{"model":%q,"plugin":true}`, model)
	if string(gotReq.Payload) != wantPayload {
		t.Fatalf("executor payload = %q, want %q", gotReq.Payload, wantPayload)
	}
	if string(gotOpts.OriginalRequest) != wantPayload {
		t.Fatalf("executor original request = %q, want %q", gotOpts.OriginalRequest, wantPayload)
	}
	if gotOpts.Headers.Get("X-Original") != "plugin" || gotOpts.Headers.Get("X-Plugin") != "1" {
		t.Fatalf("executor headers = %#v, want plugin rewrite", gotOpts.Headers)
	}
	if gotOpts.Headers.Get("X-Remove") != "" {
		t.Fatalf("executor headers kept cleared header: %#v", gotOpts.Headers)
	}
	if gotOpts.Metadata[coreexecutor.RequestedModelMetadataKey] != model {
		t.Fatalf("metadata = %#v, want requested model", gotOpts.Metadata)
	}
}

func TestHandlerRequestInterceptorEmptyBodyKeepsOriginalPayload(t *testing.T) {
	model := "handler-interceptor-empty-body-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestBeforeAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			return pluginapi.RequestInterceptResponse{
				Headers: http.Header{"X-Plugin": []string{"empty-body"}},
				Body:    []byte{},
			}
		},
	})

	originalBody := []byte(fmt.Sprintf(`{"model":%q}`, model))
	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, originalBody, "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	gotReq, gotOpts := executor.captured()
	if string(gotReq.Payload) != string(originalBody) {
		t.Fatalf("executor payload = %q, want original payload %q", gotReq.Payload, originalBody)
	}
	if gotOpts.Headers.Get("X-Plugin") != "empty-body" {
		t.Fatalf("executor headers = %#v, want plugin header", gotOpts.Headers)
	}
}

func TestHandlerRequestInterceptorAfterAuthRewritesExecutorRequest(t *testing.T) {
	model := "handler-interceptor-after-auth-model"
	executor := &interceptorCaptureExecutor{}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{})
	var calls []string
	var responseChecked bool
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestBeforeAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			calls = append(calls, "before")
			headers := cloneHeader(req.Headers)
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Stage", "before")
			return pluginapi.RequestInterceptResponse{
				Headers: headers,
				Body:    []byte(`{"stage":"before"}`),
			}
		},
		interceptRequestAfterAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			calls = append(calls, "after")
			if req.SourceFormat != "openai" || req.ToFormat != "codex" {
				t.Fatalf("request formats = %q -> %q, want openai -> codex", req.SourceFormat, req.ToFormat)
			}
			if req.Model != model || req.RequestedModel != model {
				t.Fatalf("request models = %q/%q, want %q/%q", req.Model, req.RequestedModel, model, model)
			}
			if string(req.Body) != `{"stage":"before"}` {
				t.Fatalf("after-auth body = %q, want before-auth rewrite", req.Body)
			}
			headers := cloneHeader(req.Headers)
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Stage", "after")
			return pluginapi.RequestInterceptResponse{
				Headers: headers,
				Body:    []byte(`{"stage":"after"}`),
			}
		},
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			responseChecked = true
			if req.RequestHeaders.Get("X-Stage") != "after" {
				t.Fatalf("response request headers = %#v, want after-auth header", req.RequestHeaders)
			}
			if string(req.OriginalRequest) != `{"stage":"after"}` {
				t.Fatalf("response original request = %q, want after-auth body", req.OriginalRequest)
			}
			if string(req.RequestBody) != `{"stage":"after"}` {
				t.Fatalf("response request body = %q, want after-auth body", req.RequestBody)
			}
			return pluginapi.ResponseInterceptResponse{
				Headers: cloneHeader(req.ResponseHeaders),
				Body:    cloneBytes(req.Body),
			}
		},
	})

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	if fmt.Sprint(calls) != "[before after]" {
		t.Fatalf("interceptor calls = %v, want [before after]", calls)
	}
	gotReq, gotOpts := executor.captured()
	if string(gotReq.Payload) != `{"stage":"after"}` {
		t.Fatalf("executor payload = %q, want after-auth body", gotReq.Payload)
	}
	if string(gotOpts.OriginalRequest) != `{"stage":"after"}` {
		t.Fatalf("executor original request = %q, want after-auth body", gotOpts.OriginalRequest)
	}
	if gotOpts.Headers.Get("X-Stage") != "after" {
		t.Fatalf("executor headers = %#v, want after-auth header", gotOpts.Headers)
	}
	if !responseChecked {
		t.Fatal("response interceptor was not called")
	}
}

func TestHandlerResponseInterceptorRewritesSuccessfulNonStreamResponse(t *testing.T) {
	model := "handler-interceptor-response-model"
	executor := &interceptorCaptureExecutor{
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{
				Payload: []byte("upstream-body"),
				Headers: http.Header{
					"X-Upstream": []string{"1"},
					"X-Clear":    []string{"yes"},
				},
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var responseCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			responseCalls++
			if req.StatusCode != http.StatusOK || req.Stream {
				t.Fatalf("unexpected response context: %#v", req)
			}
			if req.ResponseHeaders.Get("X-Upstream") != "1" {
				t.Fatalf("response headers = %#v, want upstream header", req.ResponseHeaders)
			}
			if string(req.Body) != "upstream-body" {
				t.Fatalf("response body = %q, want upstream-body", req.Body)
			}
			headers := cloneHeader(req.ResponseHeaders)
			headers.Set("X-Upstream", "2")
			headers.Set("X-Plugin", "response")
			headers.Del("X-Clear")
			return pluginapi.ResponseInterceptResponse{
				Headers: headers,
				Body:    []byte("plugin-body"),
			}
		},
	})

	body, headers, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "plugin-body" {
		t.Fatalf("body = %q, want plugin-body", body)
	}
	if headers.Get("X-Upstream") != "2" || headers.Get("X-Plugin") != "response" {
		t.Fatalf("headers = %#v, want plugin rewrite", headers)
	}
	if headers.Get("X-Clear") != "" {
		t.Fatalf("headers kept cleared value: %#v", headers)
	}
	if responseCalls != 1 {
		t.Fatalf("response interceptor calls = %d, want 1", responseCalls)
	}
}

func TestHandlerExecutorErrorSkipsResponseInterceptor(t *testing.T) {
	model := "handler-interceptor-error-model"
	executor := &interceptorCaptureExecutor{
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{}, &coreauth.Error{
				Code:       "upstream_failed",
				Message:    "upstream failed",
				HTTPStatus: http.StatusBadGateway,
			}
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var responseCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			responseCalls++
			return pluginapi.ResponseInterceptResponse{Body: []byte("should-not-run")}
		},
	})

	body, headers, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if errMsg == nil {
		t.Fatal("ExecuteWithAuthManager() error = nil, want upstream error")
	}
	if body != nil || headers != nil {
		t.Fatalf("body/header = %q/%#v, want nil on error", body, headers)
	}
	if responseCalls != 0 {
		t.Fatalf("response interceptor calls = %d, want 0", responseCalls)
	}
}

func TestHandlerStreamExecutorErrorSkipsResponseInterceptors(t *testing.T) {
	model := "handler-interceptor-stream-error-model"
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			return nil, &coreauth.Error{
				Code:       "stream_failed",
				Message:    "stream failed",
				HTTPStatus: http.StatusBadGateway,
			}
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var responseCalls int
	var streamCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			responseCalls++
			return pluginapi.ResponseInterceptResponse{Body: []byte("should-not-run")}
		},
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			streamCalls++
			return pluginapi.StreamChunkInterceptResponse{Body: []byte("should-not-run")}
		},
	})

	dataChan, headers, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if dataChan != nil || headers != nil {
		t.Fatalf("stream data/header = %#v/%#v, want nil on execute error", dataChan, headers)
	}
	msg, ok := <-errChan
	if !ok || msg == nil {
		t.Fatal("stream error channel did not return error message")
	}
	if msg.StatusCode != http.StatusBadGateway {
		t.Fatalf("stream error status = %d, want %d", msg.StatusCode, http.StatusBadGateway)
	}
	if responseCalls != 0 || streamCalls != 0 {
		t.Fatalf("interceptor calls = response:%d stream:%d, want 0", responseCalls, streamCalls)
	}
}

func TestHandlerStreamChunkErrorBeforePayloadSkipsResponseInterceptors(t *testing.T) {
	model := "handler-interceptor-stream-chunk-error-model"
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{
				Err: &coreauth.Error{
					Code:       "stream_failed",
					Message:    "stream failed before payload",
					HTTPStatus: http.StatusBadGateway,
				},
			}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var responseCalls int
	var streamCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			responseCalls++
			return pluginapi.ResponseInterceptResponse{Body: []byte("should-not-run")}
		},
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			streamCalls++
			return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: []byte("should-not-run")}
		},
	})

	dataChan, headers, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if dataChan == nil || errChan == nil {
		t.Fatalf("stream data/error channels = %#v/%#v, want non-nil channels", dataChan, errChan)
	}
	for chunk := range dataChan {
		t.Fatalf("unexpected stream payload before error: %q", chunk)
	}
	msg, ok := <-errChan
	if !ok || msg == nil {
		t.Fatal("stream error channel did not return error message")
	}
	if msg.StatusCode != http.StatusBadGateway {
		t.Fatalf("stream error status = %d, want %d", msg.StatusCode, http.StatusBadGateway)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected extra stream error: %+v", msg)
		}
	}
	if headers.Get("X-Upstream") != "stream" {
		t.Fatalf("headers = %#v, want original upstream headers", headers)
	}
	if responseCalls != 0 || streamCalls != 0 {
		t.Fatalf("interceptor calls = response:%d stream:%d, want 0", responseCalls, streamCalls)
	}
}

func TestHandlerStreamInterceptorRewritesAndDropsChunks(t *testing.T) {
	model := "handler-interceptor-stream-model"
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 3)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("first")}
			chunks <- coreexecutor.StreamChunk{Payload: []byte("drop")}
			chunks <- coreexecutor.StreamChunk{Payload: []byte("second")}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var streamCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptRequestBeforeAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			headers := cloneHeader(req.Headers)
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Stage", "before")
			return pluginapi.RequestInterceptResponse{
				Headers: headers,
				Body:    []byte(`{"stage":"before-stream"}`),
			}
		},
		interceptRequestAfterAuth: func(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
			if string(req.Body) != `{"stage":"before-stream"}` {
				t.Fatalf("after-auth stream body = %q, want before-auth rewrite", req.Body)
			}
			headers := cloneHeader(req.Headers)
			if headers == nil {
				headers = http.Header{}
			}
			headers.Set("X-Stage", "after")
			return pluginapi.RequestInterceptResponse{
				Headers: headers,
				Body:    []byte(`{"stage":"after-stream"}`),
			}
		},
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			streamCalls++
			if req.RequestHeaders.Get("X-Stage") != "after" {
				t.Fatalf("stream request headers = %#v, want after-auth header", req.RequestHeaders)
			}
			if string(req.OriginalRequest) != `{"stage":"after-stream"}` {
				t.Fatalf("stream original request = %q, want after-auth body", req.OriginalRequest)
			}
			if string(req.RequestBody) != `{"stage":"after-stream"}` {
				t.Fatalf("stream request body = %q, want after-auth body", req.RequestBody)
			}
			if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
				headers := cloneHeader(req.ResponseHeaders)
				headers.Set("X-Stream", "plugin")
				return pluginapi.StreamChunkInterceptResponse{Headers: headers}
			}
			if req.ResponseHeaders.Get("X-Upstream") != "stream" {
				t.Fatalf("stream response headers = %#v, want upstream header", req.ResponseHeaders)
			}
			if string(req.Body) == "drop" {
				return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
			}
			if string(req.Body) == "second" {
				if len(req.HistoryChunks) != 1 || string(req.HistoryChunks[0]) != "first|plugin" {
					t.Fatalf("history = %#v, want first transformed chunk", req.HistoryChunks)
				}
			}
			headers := cloneHeader(req.ResponseHeaders)
			headers.Set("X-Stream", "plugin")
			return pluginapi.StreamChunkInterceptResponse{
				Headers: headers,
				Body:    append(req.Body, []byte("|plugin")...),
			}
		},
	})

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
	if string(got) != "first|pluginsecond|plugin" {
		t.Fatalf("stream payload = %q, want transformed chunks without dropped chunk", got)
	}
	if upstreamHeaders.Get("X-Stream") != "plugin" {
		t.Fatalf("upstream headers = %#v, want stream plugin header", upstreamHeaders)
	}
	if streamCalls != 4 {
		t.Fatalf("stream interceptor calls = %d, want 4", streamCalls)
	}
}

func TestHandlerStreamInterceptorInitializesHeadersBeforeReturn(t *testing.T) {
	model := "handler-interceptor-stream-header-before-return-model"
	initStarted := make(chan struct{})
	allowInit := make(chan struct{})
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("payload")}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			headers := cloneHeader(req.ResponseHeaders)
			if req.ChunkIndex == pluginapi.StreamChunkHeaderInitIndex {
				close(initStarted)
				<-allowInit
				headers.Set("X-Init", "plugin")
			}
			return pluginapi.StreamChunkInterceptResponse{
				Headers: headers,
				Body:    cloneBytes(req.Body),
			}
		},
	})

	type streamResult struct {
		dataChan        <-chan []byte
		upstreamHeaders http.Header
		errChan         <-chan *interfaces.ErrorMessage
	}
	resultChan := make(chan streamResult, 1)
	go func() {
		dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
		resultChan <- streamResult{dataChan: dataChan, upstreamHeaders: upstreamHeaders, errChan: errChan}
	}()

	select {
	case result := <-resultChan:
		t.Fatalf("ExecuteStreamWithAuthManager returned before stream header init: %#v", result.upstreamHeaders)
	case <-initStarted:
	}
	select {
	case result := <-resultChan:
		t.Fatalf("ExecuteStreamWithAuthManager returned while stream header init was blocked: %#v", result.upstreamHeaders)
	default:
	}
	close(allowInit)

	result := <-resultChan
	dataChan := result.dataChan
	upstreamHeaders := result.upstreamHeaders
	errChan := result.errChan
	if upstreamHeaders.Get("X-Init") != "plugin" {
		t.Fatalf("upstream headers before first payload = %#v, want initialized plugin header", upstreamHeaders)
	}
	for range dataChan {
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
}

func TestHandlerStreamSkipsInterceptorsWhenHostReportsNoStreamInterceptors(t *testing.T) {
	model := "handler-interceptor-no-stream-capability-model"
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("payload")}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: false})
	var streamCalls int
	handler.SetPluginHost(&handlerInterceptorNoStreamTestHost{
		handlerInterceptorTestHost: &handlerInterceptorTestHost{
			interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
				streamCalls++
				return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
			},
		},
	})

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	var got []byte
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
	if string(got) != "payload" {
		t.Fatalf("stream payload = %q, want payload", got)
	}
	if upstreamHeaders != nil {
		t.Fatalf("upstream headers = %#v, want nil without passthrough or stream interceptors", upstreamHeaders)
	}
	if streamCalls != 0 {
		t.Fatalf("stream interceptor calls = %d, want 0", streamCalls)
	}
}

func TestAppendStreamInterceptorHistoryBoundsRetainedChunks(t *testing.T) {
	var history [][]byte
	for i := 0; i < maxStreamInterceptorHistoryChunks+10; i++ {
		history = appendStreamInterceptorHistory(history, []byte{byte(i)})
	}
	if len(history) != maxStreamInterceptorHistoryChunks {
		t.Fatalf("history chunks = %d, want %d", len(history), maxStreamInterceptorHistoryChunks)
	}
	if got := history[0][0]; got != 10 {
		t.Fatalf("first retained history chunk = %d, want 10", got)
	}

	history = nil
	largeChunk := make([]byte, maxStreamInterceptorHistoryBytes/2+1)
	for i := 0; i < 3; i++ {
		history = appendStreamInterceptorHistory(history, largeChunk)
	}
	if gotBytes := byteSlicesSize(history); gotBytes > maxStreamInterceptorHistoryBytes {
		t.Fatalf("history bytes = %d, want <= %d", gotBytes, maxStreamInterceptorHistoryBytes)
	}
}

func TestHandlerStreamInterceptorKeepsReturnedHeadersStableAfterFirstPayload(t *testing.T) {
	model := "handler-interceptor-stream-stable-headers-model"
	releaseSecond := make(chan struct{})
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk)
			go func() {
				defer close(chunks)
				chunks <- coreexecutor.StreamChunk{Payload: []byte("first")}
				<-releaseSecond
				chunks <- coreexecutor.StreamChunk{Payload: []byte("second")}
			}()
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			headers := cloneHeader(req.ResponseHeaders)
			switch req.ChunkIndex {
			case pluginapi.StreamChunkHeaderInitIndex:
				headers.Set("X-Stage", "init")
			case 0:
				headers.Set("X-Chunk", "first")
			case 1:
				headers.Set("X-Chunk", "second")
			}
			return pluginapi.StreamChunkInterceptResponse{
				Headers: headers,
				Body:    cloneBytes(req.Body),
			}
		},
	})

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	firstChunk, ok := <-dataChan
	if !ok {
		t.Fatal("data channel closed before first chunk")
	}
	if string(firstChunk) != "first" {
		t.Fatalf("first chunk = %q, want first", firstChunk)
	}
	if upstreamHeaders.Get("X-Chunk") != "first" || upstreamHeaders.Get("X-Stage") != "init" {
		t.Fatalf("upstream headers after first chunk = %#v, want first chunk headers", upstreamHeaders)
	}

	close(releaseSecond)
	got := append([]byte(nil), firstChunk...)
	for chunk := range dataChan {
		got = append(got, chunk...)
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
	if string(got) != "firstsecond" {
		t.Fatalf("stream payload = %q, want firstsecond", got)
	}
	if upstreamHeaders.Get("X-Chunk") != "first" {
		t.Fatalf("upstream headers changed after first payload: %#v", upstreamHeaders)
	}
}

func TestHandlerStreamInterceptorInitializesHeadersWithoutPayload(t *testing.T) {
	model := "handler-interceptor-stream-header-only-model"
	executor := &interceptorCaptureExecutor{
		stream: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("payload")}
			close(chunks)
			return &coreexecutor.StreamResult{
				Headers: http.Header{"X-Upstream": []string{"stream"}},
				Chunks:  chunks,
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: true})
	var initCalls int
	var payloadCalls int
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			if req.ChunkIndex != pluginapi.StreamChunkHeaderInitIndex {
				payloadCalls++
				if string(req.Body) != "payload" || req.ResponseHeaders.Get("X-Init") != "plugin" {
					t.Fatalf("payload stream request = %#v, want initialized headers and payload", req)
				}
				return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
			}
			initCalls++
			headers := cloneHeader(req.ResponseHeaders)
			headers.Set("X-Init", "plugin")
			return pluginapi.StreamChunkInterceptResponse{Headers: headers}
		},
	})

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	for chunk := range dataChan {
		if string(chunk) != "payload" {
			t.Fatalf("stream chunk = %q, want payload", chunk)
		}
	}
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
	if initCalls != 1 {
		t.Fatalf("initial stream calls = %d, want 1", initCalls)
	}
	if payloadCalls != 1 {
		t.Fatalf("payload stream calls = %d, want 1", payloadCalls)
	}
	if upstreamHeaders.Get("X-Init") != "plugin" {
		t.Fatalf("upstream headers = %#v, want initial plugin header", upstreamHeaders)
	}
}

func TestHandlerResponseInterceptorSeesRawHeadersWhenPassthroughDisabled(t *testing.T) {
	model := "handler-interceptor-raw-headers-model"
	executor := &interceptorCaptureExecutor{
		execute: func(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
			return coreexecutor.Response{
				Payload: []byte("upstream-body"),
				Headers: http.Header{
					"X-Upstream": []string{"raw"},
				},
			}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, &sdkconfig.SDKConfig{PassthroughHeaders: false})
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			if req.ResponseHeaders.Get("X-Upstream") != "raw" {
				t.Fatalf("response headers = %#v, want raw upstream header", req.ResponseHeaders)
			}
			headers := cloneHeader(req.ResponseHeaders)
			headers.Set("X-Plugin", "response")
			return pluginapi.ResponseInterceptResponse{Headers: headers}
		},
	})

	_, headers, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, []byte(fmt.Sprintf(`{"model":%q}`, model)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if headers.Get("X-Plugin") != "response" {
		t.Fatalf("headers = %#v, want plugin header", headers)
	}
	if headers.Get("X-Upstream") != "" {
		t.Fatalf("headers leaked raw upstream header with passthrough disabled: %#v", headers)
	}
}
