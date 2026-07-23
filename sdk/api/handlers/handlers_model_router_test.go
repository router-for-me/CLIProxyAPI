package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type handlerModelRouterTestHost struct {
	hasRouters bool
	route      func(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
	routeSkip  string
	lastReq    *pluginapi.ModelRouteRequest
}

func (h *handlerModelRouterTestHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	return h.RouteModelExcept(ctx, req, "")
}

func (h *handlerModelRouterTestHost) RouteModelExcept(ctx context.Context, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	h.routeSkip = skipPluginID
	reqCopy := req
	h.lastReq = &reqCopy
	if h != nil && h.route != nil {
		return h.route(ctx, req, skipPluginID)
	}
	return pluginapi.ModelRouteResponse{}, false
}

func (h *handlerModelRouterTestHost) HasModelRouters() bool { return h != nil && h.hasRouters }

func (h *handlerModelRouterTestHost) HasModelRoutersExcept(skipPluginID string) bool {
	return h != nil && h.hasRouters
}

func (h *handlerModelRouterTestHost) HasRequestInterceptors() bool { return false }

func (h *handlerModelRouterTestHost) HasStreamInterceptors() bool { return false }

func (h *handlerModelRouterTestHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers), Body: cloneBytes(req.Body)}
}

func (h *handlerModelRouterTestHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers), Body: cloneBytes(req.Body)}
}

func (h *handlerModelRouterTestHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

func (h *handlerModelRouterTestHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

type handlerRouterOnlyTestHost struct {
	route      func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
	hasRouters bool
	called     bool
}

func (h *handlerRouterOnlyTestHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if h != nil {
		h.called = true
	}
	if h != nil && h.route != nil {
		return h.route(ctx, req)
	}
	return pluginapi.ModelRouteResponse{}, false
}

func (h *handlerRouterOnlyTestHost) HasModelRouters() bool {
	return h != nil && h.hasRouters
}

type handlerDirectExecutorRouteHost struct {
	handlerRouterOnlyTestHost
	lastPluginID string
	lastRequest  coreexecutor.Request
	lastOptions  coreexecutor.Options
	stream       func(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
}

func (h *handlerDirectExecutorRouteHost) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	h.lastOptions = opts
	return coreexecutor.Response{Payload: []byte("direct-ok")}, nil
}

func (h *handlerDirectExecutorRouteHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	h.lastOptions = opts
	if h.stream != nil {
		return h.stream(ctx, pluginID, req, opts)
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("direct-stream")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (h *handlerDirectExecutorRouteHost) CountPluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	h.lastOptions = opts
	return coreexecutor.Response{Payload: []byte("7")}, nil
}

type handlerDirectExecutorInterceptorHost struct {
	handlerDirectExecutorRouteHost
	afterAuthCalled bool
	afterAuthReq    pluginapi.RequestInterceptRequest
}

func (h *handlerDirectExecutorInterceptorHost) HasRequestInterceptors() bool { return true }

func (h *handlerDirectExecutorInterceptorHost) HasStreamInterceptors() bool { return false }

func (h *handlerDirectExecutorInterceptorHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Headers: cloneHeader(req.Headers), Body: cloneBytes(req.Body)}
}

func (h *handlerDirectExecutorInterceptorHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.afterAuthCalled = true
	h.afterAuthReq = req
	headers := cloneHeader(req.Headers)
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("X-After-Auth", "yes")
	return pluginapi.RequestInterceptResponse{Headers: headers, Body: []byte(`{"after":true}`)}
}

func (h *handlerDirectExecutorInterceptorHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

func (h *handlerDirectExecutorInterceptorHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{Headers: cloneHeader(req.ResponseHeaders), Body: cloneBytes(req.Body)}
}

func (h *handlerDirectExecutorInterceptorHost) PluginExecutorRequestToFormat(pluginID string, req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatCodex
}

func TestHandlerModelRouterRoutesBeforeRequestDetails(t *testing.T) {
	originalModel := "handler-router-original-model"
	targetPluginID := "websearch-plugin"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.SourceFormat != "openai" || req.RequestedModel != originalModel || req.Stream {
			t.Fatalf("unexpected route request = %#v", req)
		}
		if req.Headers.Get("X-Original") != "client" {
			t.Fatalf("route headers = %#v, want client header", req.Headers)
		}
		if string(req.Body) != fmt.Sprintf(`{"model":%q}`, originalModel) {
			t.Fatalf("route body = %q, want original body", req.Body)
		}
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID, Reason: "test"}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)
	ctx := contextWithHeaders(http.Header{"X-Original": []string{"client"}})

	body, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" {
		t.Fatalf("body = %q, want direct plugin executor response", body)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("plugin id = %q, want %q", host.lastPluginID, targetPluginID)
	}
	if host.lastRequest.Model != originalModel {
		t.Fatalf("executor model = %q, want original model", host.lastRequest.Model)
	}
	if host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey] != originalModel {
		t.Fatalf("requested model metadata = %#v, want original model", host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey])
	}
}

func TestHandlerModelRouterDirectExecutorRunsAfterAuthInterceptor(t *testing.T) {
	originalModel := "handler-router-after-auth-original-model"
	targetPluginID := "websearch-plugin"
	host := &handlerDirectExecutorInterceptorHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetPluginHost(host)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" {
		t.Fatalf("body = %q, want direct plugin executor response", body)
	}
	if !host.afterAuthCalled {
		t.Fatal("after-auth interceptor was not called")
	}
	if host.afterAuthReq.SourceFormat != "openai" || host.afterAuthReq.ToFormat != "codex" {
		t.Fatalf("after-auth formats = %q -> %q, want openai -> codex", host.afterAuthReq.SourceFormat, host.afterAuthReq.ToFormat)
	}
	if host.afterAuthReq.Model != originalModel || host.afterAuthReq.RequestedModel != originalModel {
		t.Fatalf("after-auth models = %q/%q, want original model", host.afterAuthReq.Model, host.afterAuthReq.RequestedModel)
	}
	if string(host.lastRequest.Payload) != `{"after":true}` {
		t.Fatalf("executor payload = %q, want after-auth body", host.lastRequest.Payload)
	}
	if host.lastOptions.Headers.Get("X-After-Auth") != "yes" {
		t.Fatalf("executor headers = %#v, want after-auth header", host.lastOptions.Headers)
	}
	if string(host.lastOptions.OriginalRequest) != `{"after":true}` {
		t.Fatalf("original request = %q, want after-auth body", host.lastOptions.OriginalRequest)
	}
}

func TestHandlerModelRouterPluginExecutorFailsClosedWhenHomeEnabled(t *testing.T) {
	originalModel := "home-plugin-route"
	targetPluginID := "plugin-executor"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler.SetModelRouterHost(host)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(`{"model":"home-plugin-route"}`), "")
	if body != nil || errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteWithAuthManager() = %q, %#v; want 503", body, errMsg)
	}
	body, _, errMsg = handler.ExecuteCountWithAuthManager(context.Background(), "openai", originalModel, []byte(`{"model":"home-plugin-route"}`), "")
	if body != nil || errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteCountWithAuthManager() = %q, %#v; want 503", body, errMsg)
	}
	data, _, errors := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", originalModel, []byte(`{"model":"home-plugin-route","stream":true}`), "")
	if data != nil {
		t.Fatalf("ExecuteStreamWithAuthManager() data = %v, want nil", data)
	}
	if errMsg = <-errors; errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteStreamWithAuthManager() error = %#v, want 503", errMsg)
	}
	if host.lastPluginID != "" {
		t.Fatalf("plugin executor was invoked with %q while Home was enabled", host.lastPluginID)
	}
}

func TestHandlerModelRouterRequiresPluginExecutorHost(t *testing.T) {
	originalModel := "handler-router-only-original-model"
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(&handlerRouterOnlyTestHost{
		hasRouters: true,
		route: func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
			if req.RequestedModel != originalModel {
				t.Fatalf("requested model = %q, want %q", req.RequestedModel, originalModel)
			}
			return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "websearch-plugin"}, true
		},
	})

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusBadGateway {
		t.Fatalf("ExecuteWithAuthManager() error = %+v, want BadGateway", errMsg)
	}
}

func TestHandlerModelRouterCanTargetPluginExecutorWithoutChangingModel(t *testing.T) {
	originalModel := "handler-router-direct-original-model"
	targetPluginID := "websearch-plugin"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.RequestedModel != originalModel {
			t.Fatalf("requested model = %q, want %q", req.RequestedModel, originalModel)
		}
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "claude", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" {
		t.Fatalf("body = %q, want direct plugin executor response", body)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("plugin id = %q, want %q", host.lastPluginID, targetPluginID)
	}
	if host.lastRequest.Model != originalModel {
		t.Fatalf("executor model = %q, want original model", host.lastRequest.Model)
	}
	if host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey] != originalModel {
		t.Fatalf("requested model metadata = %#v, want original model", host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey])
	}
}

func TestHandlerModelRouterRoutesCountBeforeRequestDetails(t *testing.T) {
	originalModel := "handler-router-count-original-model"
	targetPluginID := "count-plugin"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.SourceFormat != "claude" || req.RequestedModel != originalModel || req.Stream {
			t.Fatalf("unexpected count route request = %#v", req)
		}
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	body, _, errMsg := handler.ExecuteCountWithAuthManager(context.Background(), "claude", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteCountWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "7" {
		t.Fatalf("body = %q, want count response", body)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("plugin id = %q, want %q", host.lastPluginID, targetPluginID)
	}
	if host.lastRequest.Model != originalModel {
		t.Fatalf("executor model = %q, want original model", host.lastRequest.Model)
	}
	if host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey] != originalModel {
		t.Fatalf("requested model metadata = %#v, want original model", host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey])
	}
}

func TestRouteModelDoesNotFallbackWhenSkipUnsupported(t *testing.T) {
	host := &handlerRouterOnlyTestHost{hasRouters: true}
	resp, ok := routeModel(context.Background(), host, pluginapi.ModelRouteRequest{RequestedModel: "model"}, "origin-plugin")
	if ok || resp.Handled {
		t.Fatalf("routeModel() = %#v, %v; want unhandled when skip is unsupported", resp, ok)
	}
	if host.called {
		t.Fatal("RouteModel was called despite unsupported skip")
	}
}

func TestApplyModelRouterSkipsHostsWithoutRouters(t *testing.T) {
	host := &handlerRouterOnlyTestHost{hasRouters: false}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	got := handler.applyModelRouter(context.Background(), "openai", "model", []byte(`{"model":"model"}`), false, modelExecutionOptions{})
	if got.ExecutorPluginID != "" {
		t.Fatalf("applyModelRouter() = %#v, want no routing decision", got)
	}
	if host.called {
		t.Fatal("RouteModel was called even though detector reported no routers")
	}
}

// routeModelOnlyHost implements PluginModelRouterHost without HasModelRouters (conservative default).
type routeModelOnlyHost struct {
	called bool
}

func (h *routeModelOnlyHost) RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if h != nil {
		h.called = true
	}
	return pluginapi.ModelRouteResponse{}, false
}

func TestModelRoutersEnabledFalseWithoutDetector(t *testing.T) {
	host := &routeModelOnlyHost{}
	if modelRoutersEnabled(host, "") {
		t.Fatal("modelRoutersEnabled() = true, want false when host has no HasModelRouters")
	}
}

func TestApplyModelRouterSkipsHostWithoutDetector(t *testing.T) {
	host := &routeModelOnlyHost{}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	got := handler.applyModelRouter(context.Background(), "openai", "model", []byte(`{"model":"model"}`), false, modelExecutionOptions{})
	if got.ExecutorPluginID != "" || got.Provider != "" {
		t.Fatalf("applyModelRouter() = %#v, want no routing decision", got)
	}
	if host.called {
		t.Fatal("RouteModel was called on host without HasModelRouters")
	}
}

func TestApplyModelRouterRestoresQueryFromContext(t *testing.T) {
	var gotQuery url.Values
	host := &handlerRouterOnlyTestHost{hasRouters: true}
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		gotQuery = cloneURLValues(req.Query)
		return pluginapi.ModelRouteResponse{}, false
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	// execOptions.Query is intentionally empty; the inbound query must be recovered
	// from the embedded gin context, mirroring plain HTTP requests.
	ctx := contextWithQuery(url.Values{"session": []string{"abc"}})
	handler.applyModelRouter(ctx, "openai", "model", []byte(`{"model":"model"}`), false, modelExecutionOptions{})

	if gotQuery.Get("session") != "abc" {
		t.Fatalf("route query = %#v, want session=abc recovered from gin context", gotQuery)
	}
}

func TestHandlerModelRouterRoutesStreamBeforeRequestDetails(t *testing.T) {
	originalModel := "handler-router-stream-original-model"
	targetPluginID := "stream-plugin"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		if req.SourceFormat != "openai" || req.RequestedModel != originalModel || !req.Stream {
			t.Fatalf("unexpected stream route request = %#v", req)
		}
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	var gotPayload bool
	for range dataChan {
		gotPayload = true
	}
	if !gotPayload {
		t.Fatal("stream produced no payload")
	}
	if errMsg := <-errChan; errMsg != nil {
		t.Fatalf("ExecuteStreamWithAuthManager() error = %+v", errMsg)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("plugin id = %q, want %q", host.lastPluginID, targetPluginID)
	}
	if host.lastRequest.Model != originalModel {
		t.Fatalf("executor model = %q, want original model", host.lastRequest.Model)
	}
	if host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey] != originalModel {
		t.Fatalf("requested model metadata = %#v, want original model", host.lastOptions.Metadata[coreexecutor.RequestedModelMetadataKey])
	}
}

func TestExecuteModelPropagatesRouterSkipPluginID(t *testing.T) {
	model := "model-execution-router-skip-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q}`, model))
	executor := &modelExecutionCaptureExecutor{}
	handler := newModelExecutionHandler(t, model, executor, &sdkconfig.SDKConfig{})
	routerHost := &handlerModelRouterTestHost{hasRouters: true}
	handler.SetPluginHost(routerHost)

	resp, errMsg := handler.ExecuteModel(context.Background(), ModelExecutionRequest{
		EntryProtocol:      "openai",
		ExitProtocol:       "openai",
		Model:              model,
		Body:               requestBody,
		SkipRouterPluginID: "origin-plugin",
	})
	if errMsg != nil {
		t.Fatalf("ExecuteModel() error = %+v", errMsg)
	}
	if string(resp.Body) != "model-execution-ok" {
		t.Fatalf("body = %q, want executor response", resp.Body)
	}
	if routerHost.routeSkip != "origin-plugin" {
		t.Fatalf("router skip id = %q, want origin-plugin", routerHost.routeSkip)
	}
}

func TestHandlerProvidersForExecutionUsesRouterProvider(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	decision := modelRouteDecision{Provider: "claude", Model: "claude-sonnet-4"}
	providers, normalizedModel, errMsg := handler.providersForExecution("ignored-by-router", "original-model", false, decision, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("providersForExecution() error = %+v", errMsg)
	}
	if fmt.Sprint(providers) != "[claude]" {
		t.Fatalf("providers = %v, want [claude]", providers)
	}
	if normalizedModel != "claude-sonnet-4" {
		t.Fatalf("normalizedModel = %q, want claude-sonnet-4", normalizedModel)
	}
}

func TestHandlerProvidersForExecutionFallsBackToOriginalModel(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	decision := modelRouteDecision{Provider: "claude"}
	providers, normalizedModel, errMsg := handler.providersForExecution("ignored-by-router", "original-model", false, decision, modelExecutionOptions{})
	if errMsg != nil {
		t.Fatalf("providersForExecution() error = %+v", errMsg)
	}
	if fmt.Sprint(providers) != "[claude]" {
		t.Fatalf("providers = %v, want [claude]", providers)
	}
	if normalizedModel != "original-model" {
		t.Fatalf("normalizedModel = %q, want original-model", normalizedModel)
	}
}

func TestHandlerModelRouterProviderRouteUsesAuthManager(t *testing.T) {
	originalModel := "provider-route-original-model"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude"}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)
	handler.AuthManager = coreauth.NewManager(nil, nil, nil)

	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	// The empty AuthManager has no claude auth, so execution surfaces an auth selection error
	// rather than succeeding. The point is that the request reached the AuthManager path.
	if errMsg == nil {
		t.Fatal("ExecuteWithAuthManager() error = nil, want auth selection error for routed provider")
	}
	if !host.called {
		t.Fatal("model router was not consulted")
	}
	if host.lastPluginID != "" {
		t.Fatalf("plugin executor path was used (plugin id = %q); want provider path via AuthManager", host.lastPluginID)
	}
}

func TestHandlerProvidersForExecutionRejectsImageOnlyModelOnProviderRoute(t *testing.T) {
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	cases := []struct {
		name          string
		originalModel string
		decision      modelRouteDecision
	}{
		{
			name:          "target-model",
			originalModel: "original-model",
			decision:      modelRouteDecision{Provider: "claude", Model: "gpt-image-2"},
		},
		{
			name:          "target-model-thinking-suffix",
			originalModel: "original-model",
			decision:      modelRouteDecision{Provider: "claude", Model: "gpt-image-2(auto)"},
		},
		{
			name:          "original-model-thinking-suffix",
			originalModel: "gpt-image-2(auto)",
			decision:      modelRouteDecision{Provider: "claude"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errMsg := handler.providersForExecution("ignored", tc.originalModel, false, tc.decision, modelExecutionOptions{})
			if errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("providersForExecution() error = %+v, want image-only service unavailable", errMsg)
			}
		})
	}
}

func TestExecuteCountWithAuthManagerPropagatesRouterSkipAndQuery(t *testing.T) {
	model := "model-execution-count-router-context-model"
	requestBody := []byte(fmt.Sprintf(`{"model":%q}`, model))
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	routerHost := &handlerModelRouterTestHost{hasRouters: true}
	handler.SetPluginHost(routerHost)
	ctx := contextWithQuery(url.Values{"session": []string{"abc"}})

	_, _, errMsg := handler.executeCountWithAuthManager(ctx, "openai", model, requestBody, "", modelExecutionOptions{
		SkipRouterPluginID: "origin-plugin",
	})
	if errMsg == nil {
		t.Fatal("executeCountWithAuthManager() error = nil, want auth selection error on empty manager")
	}
	if routerHost.routeSkip != "origin-plugin" {
		t.Fatalf("router skip id = %q, want origin-plugin", routerHost.routeSkip)
	}
	if routerHost.lastReq == nil || routerHost.lastReq.Query.Get("session") != "abc" {
		t.Fatalf("route query = %#v, want session=abc", routerHost.lastReq)
	}
}

func TestHandlerModelRouterDirectExecutorPropagatesQueryFromContext(t *testing.T) {
	originalModel := "handler-router-query-model"
	targetPluginID := "query-plugin"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)
	ctx := contextWithQuery(url.Values{"session": []string{"abc"}})

	_, _, errMsg := handler.ExecuteWithAuthManager(ctx, "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q}`, originalModel)), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if host.lastOptions.Query == nil || host.lastOptions.Query.Get("session") != "abc" {
		t.Fatalf("executor query = %#v, want session=abc from gin context", host.lastOptions.Query)
	}
}

type handlerStuckPluginStreamHost struct {
	handlerDirectExecutorRouteHost
}

func (h *handlerStuckPluginStreamHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestStreamWithPluginExecutorExitsOnContextCancel(t *testing.T) {
	originalModel := "handler-router-stream-cancel-model"
	targetPluginID := "stuck-stream-plugin"
	host := &handlerStuckPluginStreamHost{}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	handler.SetModelRouterHost(host)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dataChan, _, errChan := handler.ExecuteStreamWithAuthManager(ctx, "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-dataChan:
			if !ok {
				if errMsg := <-errChan; errMsg != nil {
					t.Fatalf("unexpected stream error: %+v", errMsg)
				}
				return
			}
		case <-deadline:
			t.Fatal("plugin executor stream goroutine did not exit after context cancel")
		}
	}
}

func TestStreamWithPluginExecutorReturnedHeadersImmutableAfterReturn(t *testing.T) {
	originalModel := "handler-router-plugin-immutable-headers-model"
	targetPluginID := "immutable-headers-plugin"
	releaseSecond := make(chan struct{})
	bodyStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	host := &handlerDirectExecutorRouteHost{}
	host.stream = func(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
		chunks := make(chan coreexecutor.StreamChunk)
		go func() {
			defer close(chunks)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("first")}
			<-releaseSecond
			chunks <- coreexecutor.StreamChunk{Payload: []byte("second")}
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	host.hasRouters = true
	host.route = func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{PassthroughHeaders: true}, nil)
	handler.SetModelRouterHost(host)
	handler.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			headers := cloneHeader(req.ResponseHeaders)
			if headers == nil {
				headers = make(http.Header)
			}
			switch req.ChunkIndex {
			case pluginapi.StreamChunkHeaderInitIndex:
				headers.Set("X-Init", "plugin")
			case 1:
				close(bodyStarted)
				<-releaseBody
				headers.Set("X-Body", "plugin")
			}
			return pluginapi.StreamChunkInterceptResponse{Headers: headers, Body: cloneBytes(req.Body)}
		},
	})

	dataChan, upstreamHeaders, errChan := handler.ExecuteStreamWithAuthManager(context.Background(), "openai", originalModel, []byte(fmt.Sprintf(`{"model":%q,"stream":true}`, originalModel)), "")
	dataDone := make(chan struct{})
	go func() {
		defer close(dataDone)
		for range dataChan {
		}
	}()
	stopReading := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopReading:
				return
			default:
				_ = upstreamHeaders.Get("X-Init")
			}
		}
	}()

	close(releaseSecond)
	<-bodyStarted
	close(releaseBody)
	<-dataDone
	for msg := range errChan {
		if msg != nil {
			t.Fatalf("unexpected stream error: %+v", msg)
		}
	}
	close(stopReading)
	<-readerDone
	if upstreamHeaders.Get("X-Init") != "plugin" || upstreamHeaders.Get("X-Body") != "" {
		t.Fatalf("returned headers mutated after return: %#v", upstreamHeaders)
	}
}

func TestQueryFromContextNilURLDoesNotPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = &http.Request{Header: make(http.Header)}
	ctx := context.WithValue(context.Background(), "gin", c)
	if got := queryFromContext(ctx); got != nil {
		t.Fatalf("queryFromContext() = %#v, want nil when URL is nil", got)
	}
}
