package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type mockModelListInterceptorHost struct {
	interceptResponse func(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse
}

func (m *mockModelListInterceptorHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}
}

func (m *mockModelListInterceptorHost) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body}
}

func (m *mockModelListInterceptorHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	if m.interceptResponse != nil {
		return m.interceptResponse(ctx, req)
	}
	return pluginapi.ResponseInterceptResponse{Headers: req.ResponseHeaders, Body: req.Body}
}

func (m *mockModelListInterceptorHost) InterceptStreamChunk(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{Headers: req.ResponseHeaders, Body: req.Body}
}

func TestModelsEndpoint_ExposesResponseToPluginInterceptors_OpenAI(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			headers := make(http.Header)
			for k, v := range req.ResponseHeaders {
				headers[k] = v
			}
			headers.Set("X-Plugin-Filtered", "true")
			transformedBody := `{"object":"list","data":[{"id":"custom-plugin-model","object":"model"}]}`
			return pluginapi.ResponseInterceptResponse{
				Headers: headers,
				Body:    []byte(transformedBody),
			}
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !intercepted {
		t.Fatal("plugin InterceptResponse was not called for /v1/models")
	}
	if capturedReq.SourceFormat != "openai" {
		t.Fatalf("captured SourceFormat = %q, want %q", capturedReq.SourceFormat, "openai")
	}
	if rr.Header().Get("X-Plugin-Filtered") != "true" {
		t.Fatalf("header X-Plugin-Filtered = %q, want %q", rr.Header().Get("X-Plugin-Filtered"), "true")
	}
	if !strings.Contains(rr.Body.String(), "custom-plugin-model") {
		t.Fatalf("response body did not contain intercepted model: %s", rr.Body.String())
	}
}

func TestModelsEndpoint_ExposesResponseToPluginInterceptors_Claude(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			headers := make(http.Header)
			for k, v := range req.ResponseHeaders {
				headers[k] = v
			}
			headers.Set("X-Plugin-Claude", "true")
			transformedBody := `{"data":[{"id":"claude-custom-filtered","object":"model"}]}`
			return pluginapi.ResponseInterceptResponse{
				Headers: headers,
				Body:    []byte(transformedBody),
			}
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !intercepted {
		t.Fatal("plugin InterceptResponse was not called for /v1/models (Claude)")
	}
	if capturedReq.SourceFormat != "claude" {
		t.Fatalf("captured SourceFormat = %q, want %q", capturedReq.SourceFormat, "claude")
	}
	if rr.Header().Get("X-Plugin-Claude") != "true" {
		t.Fatalf("header X-Plugin-Claude = %q, want %q", rr.Header().Get("X-Plugin-Claude"), "true")
	}
	if !strings.Contains(rr.Body.String(), "claude-custom-filtered") {
		t.Fatalf("response body did not contain intercepted model: %s", rr.Body.String())
	}
}

func TestModelsEndpoint_ExposesResponseToPluginInterceptors_Gemini(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			headers := make(http.Header)
			for k, v := range req.ResponseHeaders {
				headers[k] = v
			}
			headers.Set("X-Plugin-Gemini", "true")
			transformedBody := `{"models":[{"name":"models/gemini-custom","displayName":"Custom Gemini"}]}`
			return pluginapi.ResponseInterceptResponse{
				Headers: headers,
				Body:    []byte(transformedBody),
			}
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !intercepted {
		t.Fatal("plugin InterceptResponse was not called for /v1beta/models")
	}
	if capturedReq.SourceFormat != "gemini" {
		t.Fatalf("captured SourceFormat = %q, want %q", capturedReq.SourceFormat, "gemini")
	}
	if rr.Header().Get("X-Plugin-Gemini") != "true" {
		t.Fatalf("header X-Plugin-Gemini = %q, want %q", rr.Header().Get("X-Plugin-Gemini"), "true")
	}
	if !strings.Contains(rr.Body.String(), "gemini-custom") {
		t.Fatalf("response body did not contain intercepted model: %s", rr.Body.String())
	}
}

func TestModelsEndpoint_ExposesResponseToPluginInterceptors_CodexClientVersion(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			var parsed map[string]any
			if err := json.Unmarshal(req.Body, &parsed); err != nil {
				t.Fatalf("failed to unmarshal original body: %v", err)
			}
			parsed["intercepted_by_plugin"] = true
			modified, _ := json.Marshal(parsed)
			return pluginapi.ResponseInterceptResponse{
				Headers: req.ResponseHeaders,
				Body:    modified,
			}
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.137.0", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !intercepted {
		t.Fatal("plugin InterceptResponse was not called for /v1/models?client_version")
	}
	if capturedReq.SourceFormat != "openai" {
		t.Fatalf("captured SourceFormat = %q, want %q", capturedReq.SourceFormat, "openai")
	}
	if !strings.Contains(rr.Body.String(), "intercepted_by_plugin") {
		t.Fatalf("response body did not contain intercepted marker: %s", rr.Body.String())
	}
}

func TestModelsEndpoint_ExposesResponseToPluginInterceptors_GrokShell(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			var parsed map[string]any
			if err := json.Unmarshal(req.Body, &parsed); err != nil {
				t.Fatalf("failed to unmarshal grok models body: %v", err)
			}
			parsed["grok_intercepted"] = true
			modified, _ := json.Marshal(parsed)
			return pluginapi.ResponseInterceptResponse{
				Headers: req.ResponseHeaders,
				Body:    modified,
			}
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "grok-shell/1.0.0")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !intercepted {
		t.Fatal("plugin InterceptResponse was not called for /v1/models (Grok shell)")
	}
	if capturedReq.SourceFormat != "openai" {
		t.Fatalf("captured SourceFormat = %q, want %q", capturedReq.SourceFormat, "openai")
	}
	if !strings.Contains(rr.Body.String(), "grok_intercepted") {
		t.Fatalf("response body did not contain intercepted marker: %s", rr.Body.String())
	}
}

func TestModelsEndpoint_NoPluginHost_ReturnsOriginalModels(t *testing.T) {
	server := newTestServer(t)
	server.handlers.SetPluginHost(nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body = %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var parsed struct {
		Object string `json:"object"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("failed to unmarshal models response: %v", err)
	}
	if parsed.Object != "list" {
		t.Fatalf("object = %q, want list", parsed.Object)
	}
}

func TestServer_WriteModelListResponse_ExposesToInterceptors(t *testing.T) {
	server := newTestServer(t)

	var intercepted bool
	var capturedReq pluginapi.ResponseInterceptRequest

	server.handlers.SetPluginHost(&mockModelListInterceptorHost{
		interceptResponse: func(_ context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
			intercepted = true
			capturedReq = req
			return pluginapi.ResponseInterceptResponse{
				Headers: req.ResponseHeaders,
				Body:    []byte(`{"models":[{"id":"home-injected-model"}]}`),
			}
		},
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

	payload := gin.H{"models": []string{"original"}}
	server.writeModelListResponse(c, "gemini", payload)

	if !intercepted {
		t.Fatal("writeModelListResponse did not invoke plugin interceptor")
	}
	if capturedReq.SourceFormat != "gemini" {
		t.Fatalf("captured SourceFormat = %q, want gemini", capturedReq.SourceFormat)
	}
	if !strings.Contains(rec.Body.String(), "home-injected-model") {
		t.Fatalf("body did not contain intercepted model: %s", rec.Body.String())
	}
}
