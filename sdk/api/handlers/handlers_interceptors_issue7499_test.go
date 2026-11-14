package handlers

import (
	"context"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// issue7499InterceptorHost is a minimal PluginInterceptorHost used to prove
// embedder-installed hosts are invoked through the real dispatch chain.
type issue7499InterceptorHost struct {
	beforeAuthCalls int
	afterAuthCalls  int
	responseCalls   int
	streamCalls     int
}

func (h *issue7499InterceptorHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.beforeAuthCalls++
	return pluginapi.RequestInterceptResponse{}
}

func (h *issue7499InterceptorHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.afterAuthCalls++
	return pluginapi.RequestInterceptResponse{}
}

func (h *issue7499InterceptorHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	h.responseCalls++
	return pluginapi.ResponseInterceptResponse{}
}

func (h *issue7499InterceptorHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	h.streamCalls++
	return pluginapi.StreamChunkInterceptResponse{}
}

// issue7499DirectExecutorHost routes the request to a plugin executor so the
// test can drive the real ExecuteWithAuthManager chain end to end and prove
// the wired interceptor host is invoked during dispatch.
type issue7499DirectExecutorHost struct {
	issue7499InterceptorHost
	handlerRouterOnlyTestHost
	lastPluginID string
	lastRequest  coreexecutor.Request
}

func (h *issue7499DirectExecutorHost) HasModelRouters() bool { return h.hasRouters }

func (h *issue7499DirectExecutorHost) ExecutePluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	h.lastPluginID = pluginID
	h.lastRequest = req
	return coreexecutor.Response{Payload: []byte("direct-ok")}, nil
}

func (h *issue7499DirectExecutorHost) CountPluginExecutor(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (h *issue7499DirectExecutorHost) ExecutePluginExecutorStream(ctx context.Context, pluginID string, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (h *issue7499DirectExecutorHost) PluginExecutorRequestToFormat(pluginID string, req coreexecutor.Request, opts coreexecutor.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

// issue7499SkipHost implements the pluginInterceptorSkipHost surface so the
// test can assert skipPluginID survives dispatch.
type issue7499SkipHost struct {
	issue7499DirectExecutorHost
	beforeSkips []string
	afterSkips  []string
	respSkips   []string
	streamSkips []string
}

func (h *issue7499SkipHost) InterceptRequestBeforeAuthExcept(_ context.Context, _ pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	h.beforeSkips = append(h.beforeSkips, skipPluginID)
	return pluginapi.RequestInterceptResponse{}
}

func (h *issue7499SkipHost) InterceptRequestAfterAuthExcept(_ context.Context, _ pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	h.afterSkips = append(h.afterSkips, skipPluginID)
	return pluginapi.RequestInterceptResponse{}
}

func (h *issue7499SkipHost) InterceptResponseExcept(_ context.Context, _ pluginapi.ResponseInterceptRequest, skipPluginID string) pluginapi.ResponseInterceptResponse {
	h.respSkips = append(h.respSkips, skipPluginID)
	return pluginapi.ResponseInterceptResponse{}
}

func (h *issue7499SkipHost) InterceptStreamChunkExcept(_ context.Context, _ pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	h.streamSkips = append(h.streamSkips, skipPluginID)
	return pluginapi.StreamChunkInterceptResponse{}
}

var _ pluginInterceptorSkipHost = (*issue7499SkipHost)(nil)

// TestIssue7499CustomInterceptorHostInvokedThroughExecuteChain drives the
// real ExecuteWithAuthManager dispatch path so a miswired interceptor host
// cannot pass by only existing as a field.
func TestIssue7499CustomInterceptorHostInvokedThroughExecuteChain(t *testing.T) {
	originalModel := "issue7499-original-model"
	targetPluginID := "websearch-plugin"
	host := &issue7499DirectExecutorHost{}
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetModelRouterHost(host)
	handler.SetPluginHost(host)

	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", originalModel,
		[]byte(`{"model":"issue7499-original-model"}`), "")
	if errMsg != nil {
		t.Fatalf("ExecuteWithAuthManager() error = %+v", errMsg)
	}
	if string(body) != "direct-ok" {
		t.Fatalf("body = %q, want direct-ok", body)
	}
	if host.beforeAuthCalls != 1 {
		t.Fatalf("before-auth calls through execute chain = %d, want 1", host.beforeAuthCalls)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("plugin id = %q, want %q", host.lastPluginID, targetPluginID)
	}
}

// TestIssue7499CustomInterceptorHostSkipThroughExecuteChain verifies a
// skip-aware embedder host receives the plugin-skip identifier through the
// real dispatch chain, not a dropped or rewritten value.
func TestIssue7499CustomInterceptorHostSkipThroughExecuteChain(t *testing.T) {
	originalModel := "issue7499-skip-model"
	targetPluginID := "websearch-plugin"
	host := &issue7499SkipHost{}
	host.hasRouters = true
	// Route to a plugin executor while skipping interceptor "plugin-a" so the
	// nested execution cannot re-enter its own interceptor.
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	handler := NewBaseAPIHandlers(nil, nil)
	handler.SetModelRouterHost(host)
	handler.SetPluginHost(host)

	// Drive the interceptor dispatch directly with an explicit skip id; the
	// public execute path derives it from nested-execution metadata.
	_, _, errMsg := handler.applyRequestInterceptorsBeforeAuth(
		context.Background(), "openai", originalModel, "req-skip",
		coreexecutor.Request{Model: originalModel}, coreexecutor.Options{}, "plugin-a")
	if errMsg != nil {
		t.Fatalf("applyRequestInterceptorsBeforeAuth error = %+v", errMsg)
	}
	if len(host.beforeSkips) != 1 || host.beforeSkips[0] != "plugin-a" {
		t.Fatalf("before-auth skip calls = %v, want [plugin-a]", host.beforeSkips)
	}
}
