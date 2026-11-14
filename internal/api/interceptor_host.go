package api

import (
	"context"
	"fmt"
	"reflect"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// delegatedInterceptorHost forwards PluginInterceptorHost calls to an
// embedder-provided primary host while delegating model routing, plugin
// executor dispatch, request lifecycle, and websocket observer callbacks back
// to the internal plugin host. Installing this host keeps plugin model
// routing, plugin executor routes, lifecycle callbacks, and websocket
// observers functional when a custom interceptor host replaces the plugin
// host on request handlers.

type delegatedInterceptorHost struct {
	primary    handlers.PluginInterceptorHost
	pluginHost *pluginhost.Host
}

var (
	_ handlers.PluginInterceptorHost = (*delegatedInterceptorHost)(nil)
	_ handlers.PluginExecutorHost    = (*delegatedInterceptorHost)(nil)
)

// InterceptRequestBeforeAuth delegates to the embedder's host.
func (h *delegatedInterceptorHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return h.primary.InterceptRequestBeforeAuth(ctx, req)
}

// InterceptRequestBeforeAuthExcept forwards the plugin-skip identifier to the embedder's host when it
// implements the skip-aware interface, preventing plugin-initiated nested executions
// from re-entering their own interceptor; otherwise it falls back to the base method.
func (h *delegatedInterceptorHost) InterceptRequestBeforeAuthExcept(ctx context.Context, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	if skipper, ok := h.primary.(interface {
		InterceptRequestBeforeAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
	}); ok {
		return skipper.InterceptRequestBeforeAuthExcept(ctx, req, skipPluginID)
	}
	return h.primary.InterceptRequestBeforeAuth(ctx, req)
}

// InterceptRequestAfterAuth delegates to the embedder's host.
func (h *delegatedInterceptorHost) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return h.primary.InterceptRequestAfterAuth(ctx, req)
}

// InterceptRequestAfterAuthExcept forwards the plugin-skip identifier to the embedder's host when it
// implements the skip-aware interface, preventing plugin-initiated nested executions
// from re-entering their own interceptor; otherwise it falls back to the base method.
func (h *delegatedInterceptorHost) InterceptRequestAfterAuthExcept(ctx context.Context, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	if skipper, ok := h.primary.(interface {
		InterceptRequestAfterAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
	}); ok {
		return skipper.InterceptRequestAfterAuthExcept(ctx, req, skipPluginID)
	}
	return h.primary.InterceptRequestAfterAuth(ctx, req)
}

// InterceptResponse delegates to the embedder's host.
func (h *delegatedInterceptorHost) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return h.primary.InterceptResponse(ctx, req)
}

// InterceptResponseExcept forwards the plugin-skip identifier to the embedder's host when it
// implements the skip-aware interface, preventing plugin-initiated nested executions
// from re-entering their own interceptor; otherwise it falls back to the base method.
func (h *delegatedInterceptorHost) InterceptResponseExcept(ctx context.Context, req pluginapi.ResponseInterceptRequest, skipPluginID string) pluginapi.ResponseInterceptResponse {
	if skipper, ok := h.primary.(interface {
		InterceptResponseExcept(context.Context, pluginapi.ResponseInterceptRequest, string) pluginapi.ResponseInterceptResponse
	}); ok {
		return skipper.InterceptResponseExcept(ctx, req, skipPluginID)
	}
	return h.primary.InterceptResponse(ctx, req)
}

// InterceptStreamChunk delegates to the embedder's host.
func (h *delegatedInterceptorHost) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return h.primary.InterceptStreamChunk(ctx, req)
}

// InterceptStreamChunkExcept forwards the plugin-skip identifier to the embedder's host when it
// implements the skip-aware interface, preventing plugin-initiated nested executions
// from re-entering their own interceptor; otherwise it falls back to the base method.
func (h *delegatedInterceptorHost) InterceptStreamChunkExcept(ctx context.Context, req pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	if skipper, ok := h.primary.(interface {
		InterceptStreamChunkExcept(context.Context, pluginapi.StreamChunkInterceptRequest, string) pluginapi.StreamChunkInterceptResponse
	}); ok {
		return skipper.InterceptStreamChunkExcept(ctx, req, skipPluginID)
	}
	return h.primary.InterceptStreamChunk(ctx, req)
}

// RouteModel delegates to the internal plugin host so model routing stays
// functional when the interceptor host is replaced. This mirrors the
// PluginHost-as-router fallback in handlers.modelRouterHost.
func (h *delegatedInterceptorHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if h.pluginHost == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	return h.pluginHost.RouteModel(ctx, req)
}

// RouteModelExcept delegates skip-aware routing to the internal plugin host.
func (h *delegatedInterceptorHost) RouteModelExcept(ctx context.Context, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if h.pluginHost == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	return h.pluginHost.RouteModelExcept(ctx, req, skipPluginID)
}

// HasModelRouters delegates to the internal plugin host.
func (h *delegatedInterceptorHost) HasModelRouters() bool {
	return h.pluginHost != nil && h.pluginHost.HasModelRouters()
}

// HasModelRoutersExcept delegates skip-aware detection to the internal plugin host.
func (h *delegatedInterceptorHost) HasModelRoutersExcept(skipPluginID string) bool {
	return h.pluginHost != nil && h.pluginHost.HasModelRoutersExcept(skipPluginID)
}

// HasRequestInterceptors reports whether the embedder's host actually has
// request interceptors. The primary host is queried first through the optional
// detector interface; when it does not implement detection, the handler's
// default true semantics apply so the interceptor is invoked.
func (h *delegatedInterceptorHost) HasRequestInterceptors() bool {
	if h.primary == nil {
		return false
	}
	if detector, ok := h.primary.(interface{ HasRequestInterceptors() bool }); ok {
		return detector.HasRequestInterceptors()
	}
	return true
}

// HasStreamInterceptors reports whether the embedder's host has stream
// interceptors, with the same optional-detector default-true semantics.
func (h *delegatedInterceptorHost) HasStreamInterceptors() bool {
	if h.primary == nil {
		return false
	}
	if detector, ok := h.primary.(interface{ HasStreamInterceptors() bool }); ok {
		return detector.HasStreamInterceptors()
	}
	return true
}

// StreamChunkPayloadIncludesRequestBody delegates payload policy to the
// embedder's host, preserving the handler's default-true semantics when the
// primary host does not implement the policy interface.
func (h *delegatedInterceptorHost) StreamChunkPayloadIncludesRequestBody() bool {
	if h.primary == nil {
		return false
	}
	if policy, ok := h.primary.(interface{ StreamChunkPayloadIncludesRequestBody() bool }); ok {
		return policy.StreamChunkPayloadIncludesRequestBody()
	}
	return true
}

// StreamChunkPayloadIncludesHistory delegates payload policy to the embedder's
// host with the same default-true semantics.
func (h *delegatedInterceptorHost) StreamChunkPayloadIncludesHistory() bool {
	if h.primary == nil {
		return false
	}
	if policy, ok := h.primary.(interface{ StreamChunkPayloadIncludesHistory() bool }); ok {
		return policy.StreamChunkPayloadIncludesHistory()
	}
	return true
}

// CompleteRequest delegates request lifecycle completion to the internal
// plugin host, which owns plugin usage accounting and completion callbacks.
func (h *delegatedInterceptorHost) CompleteRequest(ctx context.Context, completion pluginapi.RequestCompletion) {
	if h.pluginHost == nil {
		return
	}
	h.pluginHost.CompleteRequest(ctx, completion)
}

// CompleteRequestExcept forwards the plugin-skip identifier so a plugin that
// initiated nested execution does not receive its own completion callback,
// preventing recursive dispatch.
func (h *delegatedInterceptorHost) CompleteRequestExcept(ctx context.Context, completion pluginapi.RequestCompletion, skipPluginID string) {
	if h.pluginHost == nil {
		return
	}
	h.pluginHost.CompleteRequestExcept(ctx, completion, skipPluginID)
}

// HasWebSocketResponseObservers delegates detection to the plugin host.
func (h *delegatedInterceptorHost) HasWebSocketResponseObservers() bool {
	return h.pluginHost != nil && h.pluginHost.HasWebSocketResponseObservers()
}

// ObserveWebSocketResponseEvent delegates observer dispatch to the plugin host.
func (h *delegatedInterceptorHost) ObserveWebSocketResponseEvent(ctx context.Context, event pluginapi.WebSocketResponseEvent) {
	if h.pluginHost == nil {
		return
	}
	h.pluginHost.ObserveWebSocketResponseEvent(ctx, event)
}

// ObserveWebSocketResponseEventExcept forwards the plugin-skip identifier so a
// plugin that initiated nested execution does not observe its own events.
func (h *delegatedInterceptorHost) ObserveWebSocketResponseEventExcept(ctx context.Context, event pluginapi.WebSocketResponseEvent, skipPluginID string) {
	if h.pluginHost == nil {
		return
	}
	h.pluginHost.ObserveWebSocketResponseEventExcept(ctx, event, skipPluginID)
}

// ExecutePluginExecutor delegates direct plugin-executor dispatch to the
// internal plugin host so a model route targeting a plugin executor still
// works when the interceptor host is replaced. Without this, pluginExecutorHost
// returns nil and the handler responds 502 plugin executor host is unavailable.
func (h *delegatedInterceptorHost) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	if h.pluginHost == nil {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor host is unavailable")
	}
	return h.pluginHost.ExecutePluginExecutor(ctx, pluginID, req, opts)
}

// ExecutePluginExecutorStream delegates streaming plugin-executor dispatch.
func (h *delegatedInterceptorHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if h.pluginHost == nil {
		return nil, fmt.Errorf("plugin executor host is unavailable")
	}
	return h.pluginHost.ExecutePluginExecutorStream(ctx, pluginID, req, opts)
}

// CountPluginExecutor delegates token counting to the internal plugin host.
func (h *delegatedInterceptorHost) CountPluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	if h.pluginHost == nil {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor host is unavailable")
	}
	return h.pluginHost.CountPluginExecutor(ctx, pluginID, req, opts)
}

// PluginExecutorRequestToFormat delegates executor input-format resolution to
// the internal plugin host.
func (h *delegatedInterceptorHost) PluginExecutorRequestToFormat(pluginID string, req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	if h.pluginHost == nil {
		return ""
	}
	return h.pluginHost.PluginExecutorRequestToFormat(pluginID, req, opts)
}

// normalizeInterceptorHost unwraps typed-nil PluginInterceptorHost values so
// the server can treat them as unset and avoid wrapping a nil primary.
func normalizeInterceptorHost(host handlers.PluginInterceptorHost) handlers.PluginInterceptorHost {
	if host == nil {
		return nil
	}
	v := reflect.ValueOf(host)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if v.IsNil() {
			return nil
		}
	}
	return host
}
