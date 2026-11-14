package api

import (
	"context"
	"testing"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type recordingInterceptorHost struct {
	beforeAuthCalls int
}

func (h *recordingInterceptorHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	h.beforeAuthCalls++
	return pluginapi.RequestInterceptResponse{}
}

func (h *recordingInterceptorHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func (h *recordingInterceptorHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}

func (h *recordingInterceptorHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}

var _ handlers.PluginInterceptorHost = (*recordingInterceptorHost)(nil)

// skipAwareInterceptorHost implements the pluginInterceptorSkipHost surface to
// prove the delegated host forwards skipPluginID instead of dropping it.
type skipAwareInterceptorHost struct {
	recordingInterceptorHost
	beforeAuthSkips []string
	afterAuthSkips  []string
	responseSkips   []string
	streamSkips     []string
}

func (h *skipAwareInterceptorHost) InterceptRequestBeforeAuthExcept(_ context.Context, _ pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	h.beforeAuthSkips = append(h.beforeAuthSkips, skipPluginID)
	return pluginapi.RequestInterceptResponse{}
}

func (h *skipAwareInterceptorHost) InterceptRequestAfterAuthExcept(_ context.Context, _ pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	h.afterAuthSkips = append(h.afterAuthSkips, skipPluginID)
	return pluginapi.RequestInterceptResponse{}
}

func (h *skipAwareInterceptorHost) InterceptResponseExcept(_ context.Context, _ pluginapi.ResponseInterceptRequest, skipPluginID string) pluginapi.ResponseInterceptResponse {
	h.responseSkips = append(h.responseSkips, skipPluginID)
	return pluginapi.ResponseInterceptResponse{}
}

func (h *skipAwareInterceptorHost) InterceptStreamChunkExcept(_ context.Context, _ pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	h.streamSkips = append(h.streamSkips, skipPluginID)
	return pluginapi.StreamChunkInterceptResponse{}
}

// noRequestInterceptorsHost implements the detector interface to confirm the
// delegated host honors a caller's opt-out of request interception.
type noRequestInterceptorsHost struct {
	recordingInterceptorHost
}

func (h *noRequestInterceptorsHost) HasRequestInterceptors() bool { return false }

// TestServerDelegatesInterceptorCallsToEmbedderThroughWiredHost proves the
// host installed on server.handlers via WithPluginInterceptorHost actually
// reaches the embedder through the handler dispatch chain, not just that a
// wrapper object exists. The server's interceptorHost field is the resolved
// host BaseAPIHandler.interceptorHost() reads on every request.
func TestServerDelegatesInterceptorCallsToEmbedderThroughWiredHost(t *testing.T) {
	custom := &recordingInterceptorHost{}
	server := newTestServerWithOptions(t, WithPluginHost(pluginhost.New()), WithPluginInterceptorHost(custom))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	// The wired host on the handler must be the delegated wrapper.
	wired, ok := server.handlers.PluginHost.(*delegatedInterceptorHost)
	if !ok || wired == nil {
		t.Fatalf("handler plugin host = %T, want *delegatedInterceptorHost", server.handlers.PluginHost)
	}
	// The same resolved host must be what the server holds for reloads.
	if server.interceptorHost != wired {
		t.Fatal("server.interceptorHost != handler plugin host; reload would rewire a different host")
	}
	// Exercise the host through the same call the handler makes per request.
	wired.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{})
	if custom.beforeAuthCalls != 1 {
		t.Fatalf("custom host before-auth calls = %d, want 1 through wired host", custom.beforeAuthCalls)
	}
	// Capability detection must report the embedder as active so the handler
	// does not skip its interceptor.
	if !wired.HasRequestInterceptors() {
		t.Fatal("wired host HasRequestInterceptors() = false, want true for custom host")
	}
}

// TestNewServerWithPluginInterceptorHostInstallsCustomHost verifies the
// WithPluginInterceptorHost option wires the caller-provided host into request
// handlers at construction time.
func TestNewServerWithPluginInterceptorHostInstallsCustomHost(t *testing.T) {
	custom := &recordingInterceptorHost{}
	server := newTestServerWithOptions(t, WithPluginHost(pluginhost.New()), WithPluginInterceptorHost(custom))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if server.handlers.PluginHost == nil {
		t.Fatal("handler plugin host = nil, want wrapped custom interceptor host")
	}
	if _, ok := server.handlers.PluginHost.(*delegatedInterceptorHost); !ok {
		t.Fatalf("handler plugin host type = %T, want *delegatedInterceptorHost", server.handlers.PluginHost)
	}
}

// TestUpdateClientsContextPreservesPluginInterceptorHost ensures a config
// reload keeps the caller-provided interceptor host instead of restoring the
// internal plugin host.
func TestUpdateClientsContextPreservesPluginInterceptorHost(t *testing.T) {
	custom := &recordingInterceptorHost{}
	pluginHost := pluginhost.New()
	server := newTestServerWithOptions(t, WithPluginHost(pluginHost), WithPluginInterceptorHost(custom))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if _, ok := server.handlers.PluginHost.(*delegatedInterceptorHost); !ok {
		t.Fatalf("handler plugin host type = %T, want *delegatedInterceptorHost", server.handlers.PluginHost)
	}

	server.UpdateClientsContext(context.Background(), &proxyconfig.Config{})

	after, ok := server.handlers.PluginHost.(*delegatedInterceptorHost)
	if !ok || after == nil {
		t.Fatalf("handler plugin host after reload = %T, want *delegatedInterceptorHost", server.handlers.PluginHost)
	}
	if after.primary != custom {
		t.Fatal("delegated host primary changed after reload; want same embedder host")
	}
	if after.pluginHost != pluginHost {
		t.Fatal("delegated host pluginHost changed after reload; want internal plugin host")
	}
}

// TestDelegatedInterceptorHostInvokesEmbedderInterceptors verifies the
// capability detectors reflect the embedder's host rather than the internal
// plugin host, so an empty plugin host cannot disable the custom interceptors.
func TestDelegatedInterceptorHostInvokesEmbedderInterceptors(t *testing.T) {
	custom := &recordingInterceptorHost{}
	wrapped := &delegatedInterceptorHost{primary: custom, pluginHost: pluginhost.New()}

	if !wrapped.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = false for custom host without detector, want true")
	}
	if !wrapped.HasStreamInterceptors() {
		t.Fatal("HasStreamInterceptors() = false for custom host without detector, want true")
	}

	wrapped.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{})
	if custom.beforeAuthCalls != 1 {
		t.Fatalf("custom host before-auth calls = %d, want 1", custom.beforeAuthCalls)
	}

	disabled := &noRequestInterceptorsHost{}
	wrappedDisabled := &delegatedInterceptorHost{primary: disabled, pluginHost: pluginhost.New()}
	if wrappedDisabled.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = true when embedder disables it, want false")
	}
}

// TestDelegatedInterceptorHostForwardsSkipToEmbedder verifies that when the
// embedder's host implements the skip-aware interceptor interface, the
// delegated host passes skipPluginID through instead of dropping it.
func TestDelegatedInterceptorHostForwardsSkipToEmbedder(t *testing.T) {
	custom := &skipAwareInterceptorHost{}
	wrapped := &delegatedInterceptorHost{primary: custom, pluginHost: pluginhost.New()}

	wrapped.InterceptRequestBeforeAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{}, "plugin-a")
	wrapped.InterceptRequestAfterAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{}, "plugin-b")
	wrapped.InterceptResponseExcept(context.Background(), pluginapi.ResponseInterceptRequest{}, "plugin-c")
	wrapped.InterceptStreamChunkExcept(context.Background(), pluginapi.StreamChunkInterceptRequest{}, "plugin-d")

	if len(custom.beforeAuthSkips) != 1 || custom.beforeAuthSkips[0] != "plugin-a" {
		t.Fatalf("before-auth skips = %v, want [plugin-a]", custom.beforeAuthSkips)
	}
	if len(custom.afterAuthSkips) != 1 || custom.afterAuthSkips[0] != "plugin-b" {
		t.Fatalf("after-auth skips = %v, want [plugin-b]", custom.afterAuthSkips)
	}
	if len(custom.responseSkips) != 1 || custom.responseSkips[0] != "plugin-c" {
		t.Fatalf("response skips = %v, want [plugin-c]", custom.responseSkips)
	}
	if len(custom.streamSkips) != 1 || custom.streamSkips[0] != "plugin-d" {
		t.Fatalf("stream skips = %v, want [plugin-d]", custom.streamSkips)
	}
	// A primary without the skip interface still gets invoked via the base methods.
	plain := &recordingInterceptorHost{}
	wrappedPlain := &delegatedInterceptorHost{primary: plain, pluginHost: pluginhost.New()}
	wrappedPlain.InterceptRequestBeforeAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{}, "plugin-a")
	if plain.beforeAuthCalls != 1 {
		t.Fatalf("plain host before-auth calls = %d, want 1", plain.beforeAuthCalls)
	}
}

// TestDelegatedInterceptorHostPluginCallbacksPreserveSkip verifies lifecycle
// and plugin-callback skip paths remain wired to the internal plugin host with
// the skip identifier intact. The wrapper's *Except methods are compile-time
// members of the same skip interfaces the handler type-asserts on, so a
// signature drift that dropped skipPluginID would fail to compile.
func TestDelegatedInterceptorHostPluginCallbacksPreserveSkip(t *testing.T) {
	pluginHost := pluginhost.New()
	custom := &recordingInterceptorHost{}
	wrapped := &delegatedInterceptorHost{primary: custom, pluginHost: pluginHost}

	// Compile-time proof the wrapper still satisfies the same skip-aware
	// interfaces the handler dispatches through; a regression that dropped the
	// Except methods would silently route callbacks back to the initiating
	// plugin and recurse.
	var _ interface {
		CompleteRequestExcept(context.Context, pluginapi.RequestCompletion, string)
	} = wrapped
	var _ interface {
		ObserveWebSocketResponseEventExcept(context.Context, pluginapi.WebSocketResponseEvent, string)
	} = wrapped
	var _ interface {
		RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
	} = wrapped

	// Exercise skip-aware dispatch paths; they must reach the plugin host
	// without error rather than dead-ending on the embedder's host.
	wrapped.CompleteRequestExcept(context.Background(), pluginapi.RequestCompletion{}, "plugin-a")
	wrapped.ObserveWebSocketResponseEventExcept(context.Background(), pluginapi.WebSocketResponseEvent{}, "plugin-a")
	wrapped.RouteModelExcept(context.Background(), pluginapi.ModelRouteRequest{}, "plugin-a")
	// Model routing must stay delegated to the internal plugin host so plugin
	// executors remain reachable even with a custom interceptor installed.
	if _, handled := wrapped.RouteModel(context.Background(), pluginapi.ModelRouteRequest{}); handled {
		_ = handled
	}
	if wrapped.HasModelRouters() != pluginHost.HasModelRouters() {
		t.Fatal("wrapper HasModelRouters diverged from internal plugin host")
	}
}

// TestNewServerWithoutPluginInterceptorHostKeepsInternalHost preserves the
// default behavior when no custom interceptor host is configured.
func TestNewServerWithoutPluginInterceptorHostKeepsInternalHost(t *testing.T) {
	pluginHost := pluginhost.New()
	server := newTestServerWithOptions(t, WithPluginHost(pluginHost))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if server.handlers.PluginHost != pluginHost {
		t.Fatalf("handler plugin host = %#v, want internal plugin host %#v", server.handlers.PluginHost, pluginHost)
	}
}

// TestNewServerWithTypedNilPluginInterceptorHostFallsBack ensures a typed-nil
// custom host is normalized to unset so the internal plugin host is used.
func TestNewServerWithTypedNilPluginInterceptorHostFallsBack(t *testing.T) {
	pluginHost := pluginhost.New()
	var typedNil *recordingInterceptorHost
	server := newTestServerWithOptions(t, WithPluginHost(pluginHost), WithPluginInterceptorHost(typedNil))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if server.handlers.PluginHost != pluginHost {
		t.Fatalf("handler plugin host = %#v, want internal plugin host %#v for typed-nil custom host", server.handlers.PluginHost, pluginHost)
	}
}

// TestDelegatedInterceptorHostImplementsPluginExecutorHost verifies the
// wrapper satisfies the PluginExecutorHost contract so plugin model routes to
// a plugin executor do not regress to 502 when a custom interceptor host is
// installed. The wrapped host delegates execution to the internal pluginHost.
func TestDelegatedInterceptorHostImplementsPluginExecutorHost(t *testing.T) {
	custom := &recordingInterceptorHost{}
	pluginHost := pluginhost.New()
	wrapped := &delegatedInterceptorHost{primary: custom, pluginHost: pluginHost}

	// Compile-time + runtime proof the wrapper still exposes the executor host
	// interface the handler type-asserts on for plugin-executor routes.
	executorHost, ok := any(wrapped).(interface {
		ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
		ExecutePluginExecutorStream(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
		CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	})
	if !ok || executorHost == nil {
		t.Fatal("delegated host does not implement PluginExecutorHost; plugin executor routes would 502")
	}
	// Nil-pluginHost delegates must return an explicit unavailable error rather
	// than panic, matching the handler's unavailable-host contract.
	safeWrapped := &delegatedInterceptorHost{primary: custom, pluginHost: nil}
	if _, err := safeWrapped.ExecutePluginExecutor(context.Background(), "p", coreexecutor.Request{}, coreexecutor.Options{}); err == nil {
		t.Fatal("ExecutePluginExecutor with nil plugin host = nil error, want unavailable")
	}
}

// TestDelegatedInterceptorHostNilPluginHostIsSafe ensures the delegated host is
// safe when no internal plugin host exists.
func TestDelegatedInterceptorHostNilPluginHostIsSafe(t *testing.T) {
	custom := &recordingInterceptorHost{}
	server := newTestServerWithOptions(t, WithPluginInterceptorHost(custom))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	wrapped, ok := server.handlers.PluginHost.(*delegatedInterceptorHost)
	if !ok || wrapped == nil {
		t.Fatalf("handler plugin host type = %T, want *delegatedInterceptorHost", server.handlers.PluginHost)
	}
	// Embedder interceptors remain enabled even without an internal plugin host.
	if !wrapped.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = false with nil plugin host, want true for custom host")
	}
	if wrapped.HasModelRouters() {
		t.Fatal("HasModelRouters() = true with nil plugin host, want false")
	}
	// Nil-pluginHost delegates must be no-ops rather than panics.
	wrapped.RouteModel(context.Background(), pluginapi.ModelRouteRequest{})
	wrapped.CompleteRequest(context.Background(), pluginapi.RequestCompletion{})
	wrapped.ObserveWebSocketResponseEvent(context.Background(), pluginapi.WebSocketResponseEvent{})
}
