package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func enabledPluginConfigs(ids ...string) map[string]config.PluginInstanceConfig {
	enabled := true
	configs := make(map[string]config.PluginInstanceConfig, len(ids))
	for _, id := range ids {
		configs[id] = config.PluginInstanceConfig{Enabled: &enabled}
	}
	return configs
}

func TestHostApplyConfig_DisabledGlobalSkipsSnapshot(t *testing.T) {
	loader := newTestSymbolLoader()
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: false,
			Dir:     makePluginDir(t, "alpha"),
		},
	})

	if loader.openCalls != 0 {
		t.Fatalf("Open calls = %d, want 0", loader.openCalls)
	}
	snap := h.Snapshot()
	if snap.enabled || len(snap.records) != 0 {
		t.Fatalf("Snapshot() = %+v, want empty disabled snapshot", snap)
	}
}

func TestHostApplyConfig_DisabledGlobalDoesNotResolvePluginsDir(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	})
	if !h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = false, want true before disable")
	}

	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	disabledCfg, errParseConfig := config.ParseConfigBytes([]byte(`
plugins:
  enabled: false
  dir: "~/.cli-proxy-api/plugins"
`))
	if errParseConfig != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParseConfig)
	}
	h.ApplyConfig(context.Background(), disabledCfg)

	if h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = true, want false after disable")
	}
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after global disable")
	}
	if lookup := loader.lookups["alpha"]; lookup == nil || lookup.shutdownCalls != 1 {
		shutdownCalls := 0
		if lookup != nil {
			shutdownCalls = lookup.shutdownCalls
		}
		t.Fatalf("alpha shutdown calls = %d, want 1 after global disable", shutdownCalls)
	}
	if snap := h.Snapshot(); snap.enabled || len(snap.records) != 0 {
		t.Fatalf("Snapshot() = %+v, want empty disabled snapshot", snap)
	}
}

func TestHostApplyConfigStopsDisabledPluginAndKeepsSibling(t *testing.T) {
	loader := newTestSymbolLoader()
	alphaPlugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	bravoPlugin := &testPlugin{
		registerResult:    validTestPlugin("bravo"),
		reconfigureResult: validTestPlugin("bravo"),
	}
	alphaLookup := newTestSymbolLookup(alphaPlugin)
	bravoLookup := newTestSymbolLookup(bravoPlugin)
	loader.lookups["alpha"] = alphaLookup
	loader.lookups["bravo"] = bravoLookup
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	pluginsDir := makePluginDir(t, "alpha", "bravo")
	enabledCfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: enabledPluginConfigs("alpha", "bravo"),
		},
	}
	h.ApplyConfig(context.Background(), enabledCfg)
	if !h.PluginRegistered("alpha") || !h.PluginLoaded("alpha") {
		t.Fatal("alpha was not loaded before disable")
	}
	if !h.PluginRegistered("bravo") || !h.PluginLoaded("bravo") {
		t.Fatal("bravo was not loaded before disable")
	}

	disabled := false
	stillEnabled := true
	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {Enabled: &disabled},
				"bravo": {Enabled: &stillEnabled},
			},
		},
	})

	if h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = true, want false after disable")
	}
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after disable")
	}
	if alphaLookup.shutdownCalls != 1 {
		t.Fatalf("alpha shutdown calls = %d, want 1", alphaLookup.shutdownCalls)
	}
	if !h.PluginRegistered("bravo") || !h.PluginLoaded("bravo") {
		t.Fatal("bravo was detached while disabling alpha")
	}
	if bravoLookup.shutdownCalls != 0 {
		t.Fatalf("bravo shutdown calls = %d, want 0", bravoLookup.shutdownCalls)
	}

	h.ApplyConfig(context.Background(), enabledCfg)
	if !h.PluginRegistered("alpha") || !h.PluginLoaded("alpha") {
		t.Fatal("alpha was not loaded again after re-enable")
	}
	if alphaLookup.shutdownCalls != 1 {
		t.Fatalf("alpha shutdown calls = %d, want 1 after re-enable", alphaLookup.shutdownCalls)
	}
	if alphaPlugin.registerCalls != 2 {
		t.Fatalf("alpha register calls = %d, want 2", alphaPlugin.registerCalls)
	}
	if bravoLookup.shutdownCalls != 0 || bravoPlugin.registerCalls != 1 {
		t.Fatalf("bravo changed during alpha re-enable: shutdown=%d register=%d", bravoLookup.shutdownCalls, bravoPlugin.registerCalls)
	}
}

func TestHostApplyConfig_ExpandsPluginsDirLeadingTilde(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	pluginsDir := makePluginDir(t, "alpha")
	homeDir := filepath.Dir(pluginsDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     "~/" + filepath.ToSlash(filepath.Base(pluginsDir)),
			Configs: enabledPluginConfigs("alpha"),
		},
	})

	if loader.openCalls != 1 {
		t.Fatalf("Open calls = %d, want 1", loader.openCalls)
	}
	if !h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = false, want true")
	}
}

func TestHostApplyConfig_DisabledPluginSkipsCapability(t *testing.T) {
	enabled := false
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {Enabled: &enabled},
			},
		},
	})

	if plugin.registerCalls != 0 || plugin.reconfigureCalls != 0 {
		t.Fatalf("calls = register %d reconfigure %d, want 0", plugin.registerCalls, plugin.reconfigureCalls)
	}
	if loader.openCalls != 0 {
		t.Fatalf("Open calls = %d, want 0", loader.openCalls)
	}
	if len(h.activeRecords()) != 0 {
		t.Fatalf("Snapshot records = %d, want 0", len(h.activeRecords()))
	}
}

func TestHostApplyConfig_DefaultDisabledPluginSkipsLoad(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
		},
	})

	if plugin.registerCalls != 0 || loader.openCalls != 0 {
		t.Fatalf("calls = register %d open %d, want 0", plugin.registerCalls, loader.openCalls)
	}
	if len(h.activeRecords()) != 0 {
		t.Fatalf("Snapshot records = %d, want 0", len(h.activeRecords()))
	}
}

func TestHostApplyConfigDisableStopsLoadedPluginHTTPOperations(t *testing.T) {
	disabled := false
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	lookup := newTestSymbolLookup(plugin)
	loader.lookups["alpha"] = lookup
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir := makePluginDir(t, "alpha")

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: enabledPluginConfigs("alpha"),
		},
	})

	if !h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = false, want true after load")
	}
	if !h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = false, want true after load")
	}
	if len(h.RegisteredPlugins()) != 1 {
		t.Fatalf("RegisteredPlugins() len = %d, want 1", len(h.RegisteredPlugins()))
	}
	h.mu.Lock()
	instance := h.loaded["alpha"].callbackInstance
	h.mu.Unlock()
	operationID, operation, openedOperation := h.httpOperations.open("alpha", instance, "", context.Background())
	if !openedOperation {
		t.Fatal("failed to open alpha HTTP operation before disable")
	}

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {Enabled: &disabled},
			},
		},
	})

	if len(h.RegisteredPlugins()) != 0 {
		t.Fatalf("RegisteredPlugins() len = %d, want 0 after disable", len(h.RegisteredPlugins()))
	}
	if h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = true, want false after disable")
	}
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after disable")
	}
	if lookup.shutdownCalls != 1 {
		t.Fatalf("shutdown calls = %d, want 1 after disable", lookup.shutdownCalls)
	}
	lateOpenContext := withHostCallbackIdentity(context.Background(), "alpha", instance)
	if lateOperationID, errOpen := h.openHostHTTPOperation(lateOpenContext, ""); errOpen == nil {
		h.httpOperations.cancel("alpha", instance, lateOperationID)
		t.Fatal("opened an HTTP operation after disable")
	}
	if operation.ctx.Err() != context.Canceled {
		t.Fatalf("HTTP operation context error = %v, want context.Canceled after disable", operation.ctx.Err())
	}
	h.httpOperations.mu.Lock()
	_, operationOpen := h.httpOperations.operations[hostHTTPOperationKey{pluginID: "alpha", operationID: operationID}]
	h.httpOperations.mu.Unlock()
	if operationOpen {
		t.Fatal("HTTP operation remained registered after disable")
	}
}

func TestHostUnloadPluginTargetsOnlyRequestedPlugin(t *testing.T) {
	loader := newTestSymbolLoader()
	alpha := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	bravo := &testPlugin{
		registerResult:    validTestPlugin("bravo"),
		reconfigureResult: validTestPlugin("bravo"),
	}
	alphaLookup := newTestSymbolLookup(alpha)
	bravoLookup := newTestSymbolLookup(bravo)
	loader.lookups["alpha"] = alphaLookup
	loader.lookups["bravo"] = bravoLookup
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha", "bravo"),
			Configs: enabledPluginConfigs("alpha", "bravo"),
		},
	}

	h.ApplyConfig(context.Background(), cfg)

	h.mu.Lock()
	alphaInstance := h.loaded["alpha"].callbackInstance
	bravoInstance := h.loaded["bravo"].callbackInstance
	h.mu.Unlock()
	alphaOperationID, alphaOperation, openedAlphaOperation := h.httpOperations.open("alpha", alphaInstance, "", context.Background())
	if !openedAlphaOperation {
		t.Fatal("failed to open alpha HTTP operation")
	}
	alphaCleanupDone := make(chan struct{})
	if !h.httpOperations.setCleanup("alpha", alphaOperationID, alphaOperation, func() { close(alphaCleanupDone) }) {
		t.Fatal("failed to attach alpha HTTP operation cleanup")
	}
	bravoOperationID, bravoOperation, openedBravoOperation := h.httpOperations.open("bravo", bravoInstance, "", context.Background())
	if !openedBravoOperation {
		t.Fatal("failed to open bravo HTTP operation")
	}

	if !h.UnloadPlugin("alpha") {
		t.Fatal("UnloadPlugin(alpha) = false, want true")
	}
	lateOpenContext := withHostCallbackIdentity(context.Background(), "alpha", alphaInstance)
	if lateOperationID, errOpen := h.openHostHTTPOperation(lateOpenContext, ""); errOpen == nil {
		h.httpOperations.cancel("alpha", alphaInstance, lateOperationID)
		t.Fatal("opened an HTTP operation after plugin unload started")
	}
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after targeted unload")
	}
	if !h.PluginLoaded("bravo") {
		t.Fatal("PluginLoaded(bravo) = false, want true after alpha unload")
	}
	if alphaOperation.ctx.Err() != context.Canceled {
		t.Fatalf("alpha HTTP operation context error = %v, want context.Canceled after unload", alphaOperation.ctx.Err())
	}
	select {
	case <-alphaCleanupDone:
	case <-time.After(time.Second):
		t.Fatal("alpha HTTP operation cleanup did not run after unload")
	}
	h.httpOperations.mu.Lock()
	_, alphaOperationOpen := h.httpOperations.operations[hostHTTPOperationKey{pluginID: "alpha", operationID: alphaOperationID}]
	_, bravoOperationOpen := h.httpOperations.operations[hostHTTPOperationKey{pluginID: "bravo", operationID: bravoOperationID}]
	h.httpOperations.mu.Unlock()
	if alphaOperationOpen {
		t.Fatal("alpha HTTP operation remained registered after plugin unload")
	}
	if bravoOperation.ctx.Err() != nil {
		t.Fatalf("unloading alpha canceled bravo's HTTP operation: %v", bravoOperation.ctx.Err())
	}
	if !bravoOperationOpen {
		t.Fatal("bravo HTTP operation was removed while unloading alpha")
	}
	if alphaLookup.shutdownCalls != 1 {
		t.Fatalf("alpha shutdown calls = %d, want 1", alphaLookup.shutdownCalls)
	}
	if bravoLookup.shutdownCalls != 0 {
		t.Fatalf("bravo shutdown calls = %d, want 0", bravoLookup.shutdownCalls)
	}
	plugins := h.RegisteredPlugins()
	if len(plugins) != 1 || plugins[0].ID != "bravo" {
		t.Fatalf("RegisteredPlugins() = %#v, want only bravo", plugins)
	}

	h.ApplyConfig(context.Background(), cfg)

	if loader.openCalls != 3 {
		t.Fatalf("Open calls = %d, want 3", loader.openCalls)
	}
	if alpha.registerCalls != 2 {
		t.Fatalf("alpha register calls = %d, want 2", alpha.registerCalls)
	}
	if bravo.registerCalls != 1 {
		t.Fatalf("bravo register calls = %d, want 1", bravo.registerCalls)
	}
	if bravo.reconfigureCalls != 1 {
		t.Fatalf("bravo reconfigure calls = %d, want 1", bravo.reconfigureCalls)
	}
}

func TestHostApplyConfigRegistersPluginThinkingApplier(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	plugin.registerResult.Capabilities.ThinkingApplier = testThinkingCapability{provider: "plugin-thinking"}
	plugin.reconfigureResult.Capabilities.ThinkingApplier = testThinkingCapability{provider: "plugin-thinking"}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}
	t.Cleanup(func() {
		h.ApplyConfig(context.Background(), &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Dir:     cfg.Plugins.Dir,
			},
		})
	})

	h.ApplyConfig(context.Background(), cfg)

	out, errApply := thinking.ApplyThinking([]byte(`{"model":"plugin-model"}`), "plugin-model(10240)", "openai", "plugin-thinking", "plugin-thinking")
	if errApply != nil {
		t.Fatalf("ApplyThinking() error = %v", errApply)
	}
	if got := ojson.GetBytesNoCopy(out, "thinking_budget").Int(); got != 10240 {
		t.Fatalf("thinking_budget = %d, want 10240; body=%s", got, string(out))
	}
	if got := ojson.GetBytesNoCopy(out, "plugin").String(); got != "plugin-thinking" {
		t.Fatalf("plugin = %q, want plugin-thinking; body=%s", got, string(out))
	}
}

func TestHostApplyConfigRegistersInterceptorOnlyPlugin(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "alpha",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					return pluginapi.RequestInterceptResponse{Body: []byte("registered")}, nil
				}),
			},
		},
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	})

	if len(h.activeRecords()) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.activeRecords()))
	}
}

func TestHostApplyConfigDispatchesInterceptorRPCMethods(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult: pluginapi.Plugin{
			Metadata: pluginapi.Metadata{
				Name:             "alpha",
				Version:          "1.0.0",
				Author:           "test",
				GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			},
			Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					return pluginapi.RequestInterceptResponse{Body: []byte("request|rpc")}, nil
				}),
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						return pluginapi.ResponseInterceptResponse{Body: []byte("response|rpc")}, nil
					},
				},
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						return pluginapi.StreamChunkInterceptResponse{Body: []byte("chunk|rpc")}, nil
					},
				},
			},
		},
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	})

	if len(h.activeRecords()) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.activeRecords()))
	}

	caps := h.activeRecords()[0].plugin.Capabilities
	reqResp, errReq := caps.RequestInterceptor.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("request")})
	if errReq != nil {
		t.Fatalf("InterceptRequestBeforeAuth() error = %v", errReq)
	}
	if got := string(reqResp.Body); got != "request|rpc" {
		t.Fatalf("InterceptRequestBeforeAuth() body = %q, want request|rpc", got)
	}

	respResp, errResp := caps.ResponseInterceptor.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{Body: []byte("response")})
	if errResp != nil {
		t.Fatalf("InterceptResponse() error = %v", errResp)
	}
	if got := string(respResp.Body); got != "response|rpc" {
		t.Fatalf("InterceptResponse() body = %q, want response|rpc", got)
	}

	chunkResp, errChunk := caps.StreamChunkInterceptor.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{Body: []byte("chunk")})
	if errChunk != nil {
		t.Fatalf("InterceptStreamChunk() error = %v", errChunk)
	}
	if got := string(chunkResp.Body); got != "chunk|rpc" {
		t.Fatalf("InterceptStreamChunk() body = %q, want chunk|rpc", got)
	}
}

func TestInterceptorHelpersReturnErrorsWhenCallbackMissing(t *testing.T) {
	if _, errReq := (requestInterceptorFunc(nil)).InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{}); errReq == nil {
		t.Fatal("InterceptRequestBeforeAuth() error = nil, want missing request interceptor callback")
	}
	if _, errReq := (requestInterceptorFunc(nil)).InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{}); errReq == nil {
		t.Fatal("InterceptRequestAfterAuth() error = nil, want missing request interceptor callback")
	}
	if _, errResp := (responseInterceptorFunc{interceptResponse: nil}).InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{}); errResp == nil {
		t.Fatal("InterceptResponse() error = nil, want missing response interceptor callback")
	}
	if _, errChunk := (responseInterceptorFunc{interceptStreamChunk: nil}).InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{}); errChunk == nil {
		t.Fatal("InterceptStreamChunk() error = nil, want missing stream chunk interceptor callback")
	}
}

func TestRPCInterceptorsIncludeHostCallbackID(t *testing.T) {
	client := &capturePluginClient{}
	adapter := &rpcPluginAdapter{
		host:   New(),
		client: client,
	}

	if _, errReq := adapter.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("request")}); errReq != nil {
		t.Fatalf("InterceptRequestBeforeAuth() error = %v", errReq)
	}
	var req rpcRequestInterceptRequest
	if errDecode := json.Unmarshal(client.requests[pluginabi.MethodRequestInterceptBefore], &req); errDecode != nil {
		t.Fatalf("decode request interceptor request: %v", errDecode)
	}
	if req.HostCallbackID == "" {
		t.Fatal("request interceptor before-auth host_callback_id is empty")
	}

	if _, errReq := adapter.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("request")}); errReq != nil {
		t.Fatalf("InterceptRequestAfterAuth() error = %v", errReq)
	}
	var reqAfter rpcRequestInterceptRequest
	if errDecode := json.Unmarshal(client.requests[pluginabi.MethodRequestInterceptAfter], &reqAfter); errDecode != nil {
		t.Fatalf("decode after-auth request interceptor request: %v", errDecode)
	}
	if reqAfter.HostCallbackID == "" {
		t.Fatal("request interceptor after-auth host_callback_id is empty")
	}

	if _, errResp := adapter.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{Body: []byte("response")}); errResp != nil {
		t.Fatalf("InterceptResponse() error = %v", errResp)
	}
	var resp rpcResponseInterceptRequest
	if errDecode := json.Unmarshal(client.requests[pluginabi.MethodResponseInterceptAfter], &resp); errDecode != nil {
		t.Fatalf("decode response interceptor request: %v", errDecode)
	}
	if resp.HostCallbackID == "" {
		t.Fatal("response interceptor host_callback_id is empty")
	}

	if _, errChunk := adapter.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{Body: []byte("chunk")}); errChunk != nil {
		t.Fatalf("InterceptStreamChunk() error = %v", errChunk)
	}
	var chunk rpcStreamChunkInterceptRequest
	if errDecode := json.Unmarshal(client.requests[pluginabi.MethodResponseInterceptStreamChunk], &chunk); errDecode != nil {
		t.Fatalf("decode stream chunk interceptor request: %v", errDecode)
	}
	if chunk.HostCallbackID == "" {
		t.Fatal("stream chunk interceptor host_callback_id is empty")
	}
}

func TestRPCManagementIncludesHostCallbackID(t *testing.T) {
	client := &capturePluginClient{}
	host := New()
	adapter := &rpcPluginAdapter{
		host:   host,
		client: client,
	}

	if _, errHandle := adapter.HandleManagement(context.Background(), pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/test/status",
		Body:   []byte("request"),
	}); errHandle != nil {
		t.Fatalf("HandleManagement() error = %v", errHandle)
	}
	var req rpcManagementRequest
	if errDecode := json.Unmarshal(client.requests[pluginabi.MethodManagementHandle], &req); errDecode != nil {
		t.Fatalf("decode management request: %v", errDecode)
	}
	if req.HostCallbackID == "" {
		t.Fatal("management handle host_callback_id is empty")
	}
	if req.Method != http.MethodGet || req.Path != "/v0/management/plugins/test/status" || string(req.Body) != "request" {
		t.Fatalf("management request = %#v, want forwarded request fields", req.ManagementRequest)
	}

	host.callbackContexts.mu.RLock()
	_, exists := host.callbackContexts.contexts[req.HostCallbackID]
	host.callbackContexts.mu.RUnlock()
	if exists {
		t.Fatal("management host_callback_id scope was not closed")
	}
}

func TestSanitizePluginRequestRemovesNonJSONMetadata(t *testing.T) {
	req := pluginapi.RequestInterceptRequest{
		Metadata: map[string]any{
			"keep":     "value",
			"callback": func(string) {},
			"nested": map[string]any{
				"keep": "nested",
				"drop": func() {},
			},
			"list": []any{"item", func() {}},
		},
	}
	raw, errMarshal := json.Marshal(sanitizePluginRequest(req))
	if errMarshal != nil {
		t.Fatalf("Marshal(sanitized request interceptor) error = %v", errMarshal)
	}
	var decoded pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal(sanitized request interceptor) error = %v", errUnmarshal)
	}
	if decoded.Metadata["keep"] != "value" {
		t.Fatalf("metadata keep = %#v, want value", decoded.Metadata)
	}
	if _, ok := decoded.Metadata["callback"]; ok {
		t.Fatalf("metadata callback survived sanitize: %#v", decoded.Metadata)
	}
	nested, ok := decoded.Metadata["nested"].(map[string]any)
	if !ok || nested["keep"] != "nested" {
		t.Fatalf("nested metadata = %#v, want keep", decoded.Metadata["nested"])
	}
	if _, ok := nested["drop"]; ok {
		t.Fatalf("nested metadata function survived sanitize: %#v", nested)
	}

	execReq := rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Metadata: map[string]any{
				"keep":     "value",
				"callback": func(string) {},
			},
		},
	}
	if _, errMarshalExec := json.Marshal(sanitizePluginRequest(execReq)); errMarshalExec != nil {
		t.Fatalf("Marshal(sanitized executor request) error = %v", errMarshalExec)
	}

	wrappedReq := rpcRequestInterceptRequest{
		RequestInterceptRequest: pluginapi.RequestInterceptRequest{
			Metadata: map[string]any{
				"keep":     "value",
				"callback": func(string) {},
			},
		},
		HostCallbackID: "callback-1",
	}
	if _, errMarshalWrapped := json.Marshal(sanitizePluginRequest(wrappedReq)); errMarshalWrapped != nil {
		t.Fatalf("Marshal(sanitized wrapped request interceptor) error = %v", errMarshalWrapped)
	}
}

func TestHostApplyConfig_ReconfigureCalledOnReload(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}

	h.ApplyConfig(context.Background(), cfg)
	h.ApplyConfig(context.Background(), cfg)

	if plugin.registerCalls != 1 {
		t.Fatalf("Register calls = %d, want 1", plugin.registerCalls)
	}
	if plugin.reconfigureCalls != 1 {
		t.Fatalf("Reconfigure calls = %d, want 1", plugin.reconfigureCalls)
	}
	if loader.openCalls != 1 {
		t.Fatalf("Open calls = %d, want 1", loader.openCalls)
	}
	if len(h.activeRecords()) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.activeRecords()))
	}
}

func TestHostApplyConfigLogsLoadedAndRegisteredOnlyOnInitialLoad(t *testing.T) {
	var out bytes.Buffer
	originalOut := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.GetLevel()
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	})

	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}

	h.ApplyConfig(context.Background(), cfg)
	h.ApplyConfig(context.Background(), cfg)

	logs := out.String()
	if count := strings.Count(logs, `msg="pluginhost: plugin loaded"`); count != 1 {
		t.Fatalf("plugin loaded log count = %d, want 1\n%s", count, logs)
	}
	if count := strings.Count(logs, `msg="pluginhost: plugin registered"`); count != 1 {
		t.Fatalf("plugin registered log count = %d, want 1\n%s", count, logs)
	}
	if !strings.Contains(logs, "plugin_name=alpha") {
		t.Fatalf("plugin registered log missing plugin_name:\n%s", logs)
	}
	if !strings.Contains(logs, "path=") {
		t.Fatalf("plugin logs missing path:\n%s", logs)
	}
}

func TestHostApplyConfigLogsHotReloadActiveAndRetiredVersions(t *testing.T) {
	var out bytes.Buffer
	originalOut := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.GetLevel()
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	})

	loader := newTestSymbolLoader()
	loader.lookups["alpha"] = newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("alpha"),
	})
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.4")

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": enabledPluginConfigWithStoreVersion(t, "1.0.4"),
			},
		},
	})
	paths["1.0.3"] = writeVersionedPluginFile(t, pluginsDir, "alpha", "1.0.3")
	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": enabledPluginConfigWithStoreVersion(t, "1.0.3"),
			},
		},
	})

	if !h.pluginIdentityCurrent("alpha", paths["1.0.3"], "1.0.3") {
		t.Fatalf("active plugin identity did not switch to %s", paths["1.0.3"])
	}
	if h.pluginIdentityCurrent("alpha", paths["1.0.4"], "1.0.4") {
		t.Fatalf("old plugin identity is still active: %s", paths["1.0.4"])
	}

	logs := out.String()
	if count := strings.Count(logs, `msg="pluginhost: plugin hot reloaded"`); count != 1 {
		t.Fatalf("plugin hot reloaded log count = %d, want 1\n%s", count, logs)
	}
	for _, want := range []string{
		"plugin_id=alpha",
		"active_version=1.0.3",
		"retired_version=1.0.4",
		"active_path=",
		"retired_path=",
		"alpha-v1.0.3",
		"alpha-v1.0.4",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("plugin hot reload log missing %s:\n%s", want, logs)
		}
	}
}

func TestHostApplyConfigQuiescesBeforeHotReloadRegistration(t *testing.T) {
	events := &lifecycleEventRecorder{}
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("replacement." + method)
		if method != pluginabi.MethodPluginRegister {
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}
	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	paths["2.0.0"] = writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if !h.pluginIdentityCurrent("alpha", paths["2.0.0"], "2.0.0") {
		t.Fatal("replacement plugin did not become active")
	}
	h.mu.Lock()
	retiredCount := len(h.retired["alpha"])
	h.mu.Unlock()
	if retiredCount != 1 {
		t.Fatalf("retired plugin count = %d, want 1", retiredCount)
	}
}

func TestHostApplyConfigRollsBackQuiescedPluginWhenReplacementFails(t *testing.T) {
	events := &lifecycleEventRecorder{}
	var configMu sync.Mutex
	var registeredConfig []byte
	var rollbackConfig []byte
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			var lifecycleRequest rpcLifecycleRequest
			if errUnmarshal := json.Unmarshal(request, &lifecycleRequest); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			configMu.Lock()
			if method == pluginabi.MethodPluginRegister {
				registeredConfig = bytes.Clone(lifecycleRequest.ConfigYAML)
			} else {
				rollbackConfig = bytes.Clone(lifecycleRequest.ConfigYAML)
			}
			configMu.Unlock()
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{
		call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			events.add("replacement." + method)
			if method != pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
			}
			return nil, fmt.Errorf("replacement registration failed")
		},
		shutdown: func() { events.add("replacement.shutdown") },
	}
	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
		"replacement.shutdown",
		"old." + pluginabi.MethodPluginReconfigure,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	configMu.Lock()
	gotRegisteredConfig := bytes.Clone(registeredConfig)
	gotRollbackConfig := bytes.Clone(rollbackConfig)
	configMu.Unlock()
	if !bytes.Equal(gotRollbackConfig, gotRegisteredConfig) {
		t.Fatalf("rollback config = %q, want original config %q", gotRollbackConfig, gotRegisteredConfig)
	}
	if replacementClient.shutdownCalls.Load() != 1 {
		t.Fatalf("replacement shutdown calls = %d, want 1", replacementClient.shutdownCalls.Load())
	}
	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not remain active after rollback")
	}
	h.mu.Lock()
	retiredCount := len(h.retired["alpha"])
	h.mu.Unlock()
	if retiredCount != 0 {
		t.Fatalf("retired plugin count = %d, want 0", retiredCount)
	}
}

func TestHostApplyConfigFallsBackWhenQuiesceUnsupported(t *testing.T) {
	events := &lifecycleEventRecorder{}
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCError("unknown_method", "unknown method plugin.quiesce"), nil
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("replacement." + method)
		if method != pluginabi.MethodPluginRegister {
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}
	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	paths["2.0.0"] = writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if !h.pluginIdentityCurrent("alpha", paths["2.0.0"], "2.0.0") {
		t.Fatal("unsupported quiesce prevented standard hot reload")
	}
}

func TestHostApplyConfigAbortsReplacementWhenQuiesceFails(t *testing.T) {
	events := &lifecycleEventRecorder{}
	var configMu sync.Mutex
	var registeredConfig []byte
	var rollbackConfig []byte
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			var lifecycleRequest rpcLifecycleRequest
			if errUnmarshal := json.Unmarshal(request, &lifecycleRequest); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			configMu.Lock()
			if method == pluginabi.MethodPluginRegister {
				registeredConfig = bytes.Clone(lifecycleRequest.ConfigYAML)
			} else {
				rollbackConfig = bytes.Clone(lifecycleRequest.ConfigYAML)
			}
			configMu.Unlock()
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCError("quiesce_failed", "sqlite connection busy"), nil
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("replacement." + method)
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}
	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	paths["2.0.0"] = writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"old." + pluginabi.MethodPluginReconfigure,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if loader.calls != 1 {
		t.Fatalf("loader Open calls = %d, want 1 (replacement must not be opened)", loader.calls)
	}
	configMu.Lock()
	gotRegisteredConfig := bytes.Clone(registeredConfig)
	gotRollbackConfig := bytes.Clone(rollbackConfig)
	configMu.Unlock()
	if !bytes.Equal(gotRollbackConfig, gotRegisteredConfig) {
		t.Fatalf("rollback config = %q, want original config %q", gotRollbackConfig, gotRegisteredConfig)
	}
	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not remain active after quiesce failure")
	}
	if h.pluginIdentityCurrent("alpha", paths["2.0.0"], "2.0.0") {
		t.Fatal("replacement plugin unexpectedly became active after quiesce failure")
	}
	h.mu.Lock()
	loadingCount := len(h.loading)
	retiredCount := len(h.retired["alpha"])
	h.mu.Unlock()
	if loadingCount != 0 {
		t.Fatalf("loading plugin count = %d, want 0", loadingCount)
	}
	if retiredCount != 0 {
		t.Fatalf("retired plugin count = %d, want 0", retiredCount)
	}
}

func TestHostApplyConfigCoordinatesUsageAcrossQuiesceAndReplacement(t *testing.T) {
	events := &lifecycleEventRecorder{}
	inFlightStarted := make(chan struct{})
	releaseInFlight := make(chan struct{})
	inFlightDone := make(chan struct{})
	drainWaitEntered := make(chan struct{})
	quiesceStarted := make(chan struct{})
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})

	var oldQuiesced atomic.Bool
	var oldRejectedUsage atomic.Int32
	var oldDelivered []string
	var replacementDelivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			events.add("old." + method)
			oldQuiesced.Store(true)
			close(quiesceStarted)
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if oldQuiesced.Load() {
				oldRejectedUsage.Add(1)
				events.add("old.usage.rejected:" + rec.RequestID)
				return marshalRPCError("quiesced", "database closed during quiesce"), nil
			}
			if rec.RequestID == "in-flight-before-quiesce" {
				close(inFlightStarted)
				<-releaseInFlight
			}
			deliveryMu.Lock()
			oldDelivered = append(oldDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("old.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("replacement." + method)
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			deliveryMu.Lock()
			replacementDelivered = append(replacementDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("replacement.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	var drainWaitOnce sync.Once
	h.usageDrainWaitHook = func(pluginID string) {
		if pluginID == "alpha" {
			drainWaitOnce.Do(func() { close(drainWaitEntered) })
		}
	}
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	staleOldPlugin := h.currentUsagePlugin("alpha")
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   staleOldPlugin,
	}

	go func() {
		adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "in-flight-before-quiesce"})
		close(inFlightDone)
	}()
	waitForHostTestSignal(t, inFlightStarted, "in-flight usage before quiesce")

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, drainWaitEntered, "usage drain wait before quiesce")
	select {
	case <-quiesceStarted:
		t.Fatal("plugin.quiesce started before already-admitted usage call drained")
	default:
	}

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-during-drain"})

	close(releaseInFlight)
	waitForHostTestSignal(t, inFlightDone, "in-flight usage completion")
	waitForHostTestSignal(t, quiesceStarted, "plugin quiesce start")
	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-1"})
	staleOldPlugin.HandleUsage(context.Background(), pluginapi.UsageRecord{RequestID: "held-2-via-stale-ref"})

	close(releaseReplacementRegister)
	waitForHostTestSignal(t, applyDone, "replacement ApplyConfig completion")

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "post-reload-3"})

	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiesced old plugin = %d, want 0 (events=%v)", got, events.snapshot())
	}
	deliveryMu.Lock()
	gotOld := append([]string(nil), oldDelivered...)
	gotReplacement := append([]string(nil), replacementDelivered...)
	deliveryMu.Unlock()
	if wantOld := []string{"in-flight-before-quiesce"}; !slices.Equal(gotOld, wantOld) {
		t.Fatalf("old plugin delivered usage = %v, want %v", gotOld, wantOld)
	}
	if wantReplacement := []string{"held-during-drain", "held-1", "held-2-via-stale-ref", "post-reload-3"}; !slices.Equal(gotReplacement, wantReplacement) {
		t.Fatalf("replacement plugin delivered usage = %v, want %v", gotReplacement, wantReplacement)
	}
}

func TestHostApplyConfigReplaysHeldUsageToRestoredPluginWhenReplacementFails(t *testing.T) {
	events := &lifecycleEventRecorder{}
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})

	var oldQuiesced atomic.Bool
	var oldRejectedUsage atomic.Int32
	var oldDelivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			events.add("old." + method)
			oldQuiesced.Store(true)
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodPluginReconfigure:
			events.add("old." + method)
			oldQuiesced.Store(false)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if oldQuiesced.Load() {
				oldRejectedUsage.Add(1)
				events.add("old.usage.rejected:" + rec.RequestID)
				return marshalRPCError("quiesced", "database closed during quiesce"), nil
			}
			deliveryMu.Lock()
			oldDelivered = append(oldDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("old.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{
		call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			events.add("replacement." + method)
			if method != pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
			}
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return nil, fmt.Errorf("replacement registration failed")
		},
		shutdown: func() { events.add("replacement.shutdown") },
	}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-rollback-1"})
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-rollback-2"})

	close(releaseReplacementRegister)
	waitForHostTestSignal(t, applyDone, "failed replacement ApplyConfig completion")

	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiesced old plugin = %d, want 0 (events=%v)", got, events.snapshot())
	}
	deliveryMu.Lock()
	gotOld := append([]string(nil), oldDelivered...)
	deliveryMu.Unlock()
	if wantOld := []string{"held-rollback-1", "held-rollback-2"}; !slices.Equal(gotOld, wantOld) {
		t.Fatalf("restored old plugin delivered usage = %v, want %v (events=%v)", gotOld, wantOld, events.snapshot())
	}
	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
		"replacement.shutdown",
		"old." + pluginabi.MethodPluginReconfigure,
		"old.usage.handle:held-rollback-1",
		"old.usage.handle:held-rollback-2",
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestHostApplyConfigReplaysHeldUsageWhenQuiesceFails(t *testing.T) {
	events := &lifecycleEventRecorder{}
	quiesceStarted := make(chan struct{})
	releaseQuiesce := make(chan struct{})

	var oldQuiescing atomic.Bool
	var oldRejectedUsage atomic.Int32
	var oldDelivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			events.add("old." + method)
			oldQuiescing.Store(true)
			close(quiesceStarted)
			<-releaseQuiesce
			return marshalRPCError("quiesce_failed", "failed to flush state"), nil
		case pluginabi.MethodPluginReconfigure:
			events.add("old." + method)
			oldQuiescing.Store(false)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if oldQuiescing.Load() {
				oldRejectedUsage.Add(1)
				events.add("old.usage.rejected:" + rec.RequestID)
				return marshalRPCError("quiesced", "database closed during quiesce"), nil
			}
			deliveryMu.Lock()
			oldDelivered = append(oldDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("old.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("replacement." + method)
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}

	backpressureEntered := make(chan struct{})
	var backpressureOnce sync.Once
	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	h.maxPendingUsageRecords = 1
	h.usageBackpressureWaitHook = func(pluginID string) {
		if pluginID == "alpha" {
			backpressureOnce.Do(func() { close(backpressureEntered) })
		}
	}
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, quiesceStarted, "plugin quiesce start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-quiesce-fail-1"})

	secondSubmitted := make(chan struct{})
	go func() {
		adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-quiesce-fail-2"})
		close(secondSubmitted)
	}()
	waitForHostTestSignal(t, backpressureEntered, "usage backpressure wait")
	select {
	case <-secondSubmitted:
		t.Fatal("second usage event bypassed bounded pending queue backpressure")
	default:
	}

	close(releaseQuiesce)
	waitForHostTestSignal(t, applyDone, "failed quiesce ApplyConfig completion")
	waitForHostTestSignal(t, secondSubmitted, "backpressured usage submission completion")

	if loader.calls != 1 {
		t.Fatalf("loader Open calls = %d, want 1", loader.calls)
	}
	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiescing old plugin = %d, want 0 (events=%v)", got, events.snapshot())
	}
	deliveryMu.Lock()
	gotOld := append([]string(nil), oldDelivered...)
	deliveryMu.Unlock()
	if wantOld := []string{"held-quiesce-fail-1", "held-quiesce-fail-2"}; !slices.Equal(gotOld, wantOld) {
		t.Fatalf("restored old plugin delivered usage = %v, want %v", gotOld, wantOld)
	}
}

func TestHostApplyConfigRetainsHeldUsageWhenRollbackFailsUntilRecovery(t *testing.T) {
	events := &lifecycleEventRecorder{}
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})

	var oldQuiesced atomic.Bool
	var allowReconfigure atomic.Bool
	var oldRejectedUsage atomic.Int32
	var oldDelivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			events.add("old." + method)
			oldQuiesced.Store(true)
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodPluginReconfigure:
			events.add("old." + method)
			if !allowReconfigure.Load() {
				return marshalRPCError("reconfigure_failed", "sqlite lock still busy"), nil
			}
			oldQuiesced.Store(false)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if oldQuiesced.Load() {
				oldRejectedUsage.Add(1)
				events.add("old.usage.rejected:" + rec.RequestID)
				return marshalRPCError("quiesced", "database closed during quiesce"), nil
			}
			deliveryMu.Lock()
			oldDelivered = append(oldDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("old.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{
		call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			events.add("replacement." + method)
			if method != pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
			}
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return nil, fmt.Errorf("replacement registration failed")
		},
		shutdown: func() { events.add("replacement.shutdown") },
	}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-double-fail-1"})
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-double-fail-2"})

	close(releaseReplacementRegister)
	waitForHostTestSignal(t, applyDone, "failed replacement and failed rollback ApplyConfig")

	if h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("quiesced old plugin remained active after rollback reconfigure failed")
	}
	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiesced old plugin after failed rollback = %d, want 0 (events=%v)", got, events.snapshot())
	}

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-while-unrecovered-3"})
	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiesced old plugin while unrecovered = %d, want 0", got)
	}

	allowReconfigure.Store(true)
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))

	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not become active after recovery reconfigure")
	}
	deliveryMu.Lock()
	gotOld := append([]string(nil), oldDelivered...)
	deliveryMu.Unlock()
	if wantOld := []string{"held-double-fail-1", "held-double-fail-2", "held-while-unrecovered-3"}; !slices.Equal(gotOld, wantOld) {
		t.Fatalf("recovered old plugin delivered usage = %v, want %v (events=%v)", gotOld, wantOld, events.snapshot())
	}
}

func TestHostApplyConfigWaitsForBlockedQuiesceBeforeCanceledRollbackAndReplay(t *testing.T) {
	events := &lifecycleEventRecorder{}
	quiesceStarted := make(chan struct{})
	releaseQuiesce := make(chan struct{})
	reconfigureStarted := make(chan struct{})
	var releaseQuiesceOnce sync.Once
	t.Cleanup(func() {
		releaseQuiesceOnce.Do(func() { close(releaseQuiesce) })
	})

	var oldQuiesced atomic.Bool
	var oldRejectedUsage atomic.Int32
	var oldDelivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			events.add("old.quiesce.start")
			close(quiesceStarted)
			<-releaseQuiesce
			oldQuiesced.Store(true)
			events.add("old.quiesce.finish")
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodPluginReconfigure:
			var reconfigureOnce sync.Once
			_ = reconfigureOnce
			select {
			case <-reconfigureStarted:
			default:
				close(reconfigureStarted)
			}
			oldQuiesced.Store(false)
			events.add("old." + method)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if oldQuiesced.Load() {
				oldRejectedUsage.Add(1)
				events.add("old.usage.rejected:" + rec.RequestID)
				return marshalRPCError("quiesced", "database closed during quiesce"), nil
			}
			deliveryMu.Lock()
			oldDelivered = append(oldDelivered, rec.RequestID)
			deliveryMu.Unlock()
			events.add("old.usage.handle:" + rec.RequestID)
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("replacement." + method)
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	baseCtx, cancel := context.WithCancel(context.Background())
	cancelObserved := make(chan struct{})
	var cancelObservedOnce sync.Once
	ctx := &errObserveContext{
		Context: baseCtx,
		onCanceledErr: func() {
			cancelObservedOnce.Do(func() { close(cancelObserved) })
		},
	}
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, quiesceStarted, "plugin quiesce start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-during-canceled-quiesce"})
	cancel()
	waitForHostTestSignal(t, cancelObserved, "quiesce call observed context cancellation")

	releaseQuiesceOnce.Do(func() { close(releaseQuiesce) })
	waitForHostTestSignal(t, applyDone, "canceled quiesce ApplyConfig completion")

	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not remain active after canceled quiesce rollback")
	}
	if got := oldRejectedUsage.Load(); got != 0 {
		t.Fatalf("usage calls reaching quiesced old plugin = %d, want 0 (events=%v)", got, events.snapshot())
	}
	deliveryMu.Lock()
	gotOld := append([]string(nil), oldDelivered...)
	deliveryMu.Unlock()
	if wantOld := []string{"held-during-canceled-quiesce"}; !slices.Equal(gotOld, wantOld) {
		t.Fatalf("restored old plugin delivered usage = %v, want %v", gotOld, wantOld)
	}
	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old.quiesce.start",
		"old.quiesce.finish",
		"old." + pluginabi.MethodPluginReconfigure,
		"old.usage.handle:held-during-canceled-quiesce",
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestHostApplyConfigReplayDoesNotStarveUnderContinuousUsageTraffic(t *testing.T) {
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})
	heldReplayStarted := make(chan struct{})
	releaseHeldReplay := make(chan struct{})
	postReplayBlocked := make(chan struct{})
	releasePostReplay := make(chan struct{})
	postReplayDone := make(chan struct{})
	var heldReplayOnce sync.Once
	var postReplayOnce sync.Once
	var postDoneOnce sync.Once

	var delivered []string
	var deliveryMu sync.Mutex

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if rec.RequestID == "held-1" {
				heldReplayOnce.Do(func() { close(heldReplayStarted) })
				<-releaseHeldReplay
			}
			if rec.RequestID == "post-during-replay" {
				postReplayOnce.Do(func() { close(postReplayBlocked) })
				<-releasePostReplay
			}
			deliveryMu.Lock()
			delivered = append(delivered, rec.RequestID)
			deliveryMu.Unlock()
			if rec.RequestID == "post-during-replay" {
				postDoneOnce.Do(func() { close(postReplayDone) })
			}
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	var releaseHeldOnce sync.Once
	var releasePostOnce sync.Once
	t.Cleanup(func() {
		releaseHeldOnce.Do(func() { close(releaseHeldReplay) })
		releasePostOnce.Do(func() { close(releasePostReplay) })
	})
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-1"})

	close(releaseReplacementRegister)
	waitForHostTestSignal(t, heldReplayStarted, "held usage replay start")

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "post-during-replay"})

	releaseHeldOnce.Do(func() { close(releaseHeldReplay) })
	waitForHostTestSignal(t, applyDone, "ApplyConfig completion without waiting for post-replay usage")
	waitForHostTestSignal(t, postReplayBlocked, "post-replay usage dispatch")
	releasePostOnce.Do(func() { close(releasePostReplay) })
	waitForHostTestSignal(t, postReplayDone, "post-replay usage delivery completion")

	deliveryMu.Lock()
	gotDelivered := append([]string(nil), delivered...)
	deliveryMu.Unlock()
	if wantDelivered := []string{"held-1", "post-during-replay"}; !slices.Equal(gotDelivered, wantDelivered) {
		t.Fatalf("delivered usage = %v, want %v", gotDelivered, wantDelivered)
	}
}

func TestHostApplyConfigRetriesFailedQuiesceOnSubsequentReplacement(t *testing.T) {
	var quiesceCalls atomic.Int32
	var replacementRegisters atomic.Int32
	var failQuiesce atomic.Bool
	var failReconfigure atomic.Bool
	failQuiesce.Store(true)
	failReconfigure.Store(true)

	var deliveryMu sync.Mutex
	delivered := make([]string, 0, 2)

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			quiesceCalls.Add(1)
			if failQuiesce.Load() {
				return marshalRPCError("quiesce_failed", "sqlite connection busy"), nil
			}
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodPluginReconfigure:
			if failReconfigure.Load() {
				return marshalRPCError("reconfigure_failed", "sqlite lock still busy"), nil
			}
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			deliveryMu.Lock()
			delivered = append(delivered, "old:"+rec.RequestID)
			deliveryMu.Unlock()
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			replacementRegisters.Add(1)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			deliveryMu.Lock()
			delivered = append(delivered, "new:"+rec.RequestID)
			deliveryMu.Unlock()
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "before"})

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
	if gotQuiesce := quiesceCalls.Load(); gotQuiesce != 1 {
		t.Fatalf("quiesce calls after first failed replacement = %d, want 1", gotQuiesce)
	}
	if gotRegisters := replacementRegisters.Load(); gotRegisters != 0 {
		t.Fatalf("replacement register count after first failed replacement = %d, want 0", gotRegisters)
	}

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-after-failed-quiesce"})

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
	if gotQuiesce := quiesceCalls.Load(); gotQuiesce != 2 {
		t.Fatalf("quiesce calls after second failed replacement = %d, want 2", gotQuiesce)
	}
	if gotRegisters := replacementRegisters.Load(); gotRegisters != 0 {
		t.Fatalf("replacement register count after second failed replacement = %d, want 0 (replacement must not load while quiesce still fails)", gotRegisters)
	}

	failQuiesce.Store(false)
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
	if gotQuiesce := quiesceCalls.Load(); gotQuiesce != 3 {
		t.Fatalf("quiesce calls after third replacement = %d, want 3", gotQuiesce)
	}
	if gotRegisters := replacementRegisters.Load(); gotRegisters != 1 {
		t.Fatalf("replacement register count after third replacement = %d, want 1", gotRegisters)
	}

	deliveryMu.Lock()
	gotDelivered := append([]string(nil), delivered...)
	deliveryMu.Unlock()
	if wantDelivered := []string{"old:before", "new:held-after-failed-quiesce"}; !slices.Equal(gotDelivered, wantDelivered) {
		t.Fatalf("delivered usage = %v, want %v", gotDelivered, wantDelivered)
	}
}

func TestHostApplyConfigRecoversFromTransientUsageReplayErrorWithoutReapply(t *testing.T) {
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})
	heldReplayStarted := make(chan struct{})
	releaseHeldReplay := make(chan struct{})
	asyncErrorObserved := make(chan struct{})
	allDelivered := make(chan struct{})
	var heldReplayOnce sync.Once
	var asyncErrorOnce sync.Once
	var deliveredOnce sync.Once
	var failFirstHeldAttempt atomic.Bool
	var failFirstAsyncAttempt atomic.Bool
	failFirstHeldAttempt.Store(true)
	failFirstAsyncAttempt.Store(true)

	var deliveryMu sync.Mutex
	delivered := make([]string, 0, 3)

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodUsageHandle:
			var rec pluginapi.UsageRecord
			if errUnmarshal := json.Unmarshal(request, &rec); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			if rec.RequestID == "held-1" {
				heldReplayOnce.Do(func() { close(heldReplayStarted) })
				<-releaseHeldReplay
				if failFirstHeldAttempt.CompareAndSwap(true, false) {
					return marshalRPCError("transient_error", "temporary sync replay failure"), nil
				}
			}
			if rec.RequestID == "async-1" && failFirstAsyncAttempt.CompareAndSwap(true, false) {
				asyncErrorOnce.Do(func() { close(asyncErrorObserved) })
				return marshalRPCError("transient_error", "temporary write failure"), nil
			}
			deliveryMu.Lock()
			delivered = append(delivered, rec.RequestID)
			if len(delivered) == 3 {
				deliveredOnce.Do(func() { close(allDelivered) })
			}
			deliveryMu.Unlock()
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	var releaseHeldOnce sync.Once
	t.Cleanup(func() {
		releaseHeldOnce.Do(func() { close(releaseHeldReplay) })
	})
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	adapter := &usageAdapter{
		host:     h,
		pluginID: "alpha",
		plugin:   h.currentUsagePlugin("alpha"),
	}

	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "held-1"})
	close(releaseReplacementRegister)

	waitForHostTestSignal(t, heldReplayStarted, "held usage replay start")
	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "async-1"})
	releaseHeldOnce.Do(func() { close(releaseHeldReplay) })

	waitForHostTestSignal(t, applyDone, "ApplyConfig completion")
	waitForHostTestSignal(t, asyncErrorObserved, "first async replay transient error")

	adapter.HandleUsage(context.Background(), coreusage.Record{RequestID: "after-1"})
	waitForHostTestSignal(t, allDelivered, "retry of async-1 and delivery of after-1 without re-applying config")

	deliveryMu.Lock()
	gotDelivered := append([]string(nil), delivered...)
	deliveryMu.Unlock()
	if wantDelivered := []string{"held-1", "async-1", "after-1"}; !slices.Equal(gotDelivered, wantDelivered) {
		t.Fatalf("delivered usage = %v, want %v", gotDelivered, wantDelivered)
	}
}

func TestHostApplyConfigWaitsForCanceledRecoveryReconfigureAndQuiescesAgainOnNextReplacement(t *testing.T) {
	var quiesceCalls atomic.Int32
	var reconfigureCalls atomic.Int32
	var resourceOpen atomic.Bool
	var overlapDetected atomic.Bool

	recoveryReconfigureStarted := make(chan struct{})
	releaseRecoveryReconfigure := make(chan struct{})
	var recoveryStartedOnce sync.Once
	var releaseRecoveryOnce sync.Once

	oldClient := &lifecycleTestClient{call: func(ctx context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			resourceOpen.Store(true)
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			quiesceCalls.Add(1)
			resourceOpen.Store(false)
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodPluginReconfigure:
			callNum := reconfigureCalls.Add(1)
			resourceOpen.Store(true)
			if callNum == 1 {
				return marshalRPCError("reconfigure_failed", "failed after reopening db"), nil
			}
			recoveryStartedOnce.Do(func() { close(recoveryReconfigureStarted) })
			<-releaseRecoveryReconfigure
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	failingReplacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		if method != pluginabi.MethodPluginRegister {
			return nil, fmt.Errorf("unexpected failing replacement method %s", method)
		}
		return nil, fmt.Errorf("first replacement registration failed")
	}}
	workingReplacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		if method != pluginabi.MethodPluginRegister {
			return nil, fmt.Errorf("unexpected working replacement method %s", method)
		}
		if resourceOpen.Load() {
			overlapDetected.Store(true)
		}
		return lifecycleRegistrationResult(validTestPlugin("alpha"))
	}}

	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, failingReplacementClient, workingReplacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	t.Cleanup(func() {
		releaseRecoveryOnce.Do(func() { close(releaseRecoveryReconfigure) })
	})
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	// Attempt 1: quiesce succeeds, replacement fails, rollback reconfigure reopens resource and fails.
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
	if got := quiesceCalls.Load(); got != 1 {
		t.Fatalf("quiesce calls after first replacement = %d, want 1", got)
	}
	if !resourceOpen.Load() {
		t.Fatal("expected rollback reconfigure to leave simulated resource open after partial failure")
	}

	// Attempt 2: same-path recovery reconfigure is canceled while blocked in native call.
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	recoveryDone := make(chan struct{})
	go func() {
		h.ApplyConfig(recoveryCtx, versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		close(recoveryDone)
	}()
	waitForHostTestSignal(t, recoveryReconfigureStarted, "recovery reconfigure start")
	cancelRecovery()

	select {
	case <-recoveryDone:
		t.Fatal("ApplyConfig returned before canceled recovery reconfigure finished executing")
	case <-time.After(50 * time.Millisecond):
	}
	releaseRecoveryOnce.Do(func() { close(releaseRecoveryReconfigure) })
	waitForHostTestSignal(t, recoveryDone, "canceled recovery ApplyConfig completion")

	// Attempt 3: replacement must call quiesce again before loading workingReplacementClient.
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
	if got := quiesceCalls.Load(); got != 2 {
		t.Fatalf("quiesce calls after second replacement = %d, want 2", got)
	}
	if overlapDetected.Load() {
		t.Fatal("replacement registered while old plugin still held reopened resource")
	}
}

func TestHostCallQuiesceClassifiesErrors(t *testing.T) {
	var out bytes.Buffer
	originalOut := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.GetLevel()
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.DebugLevel)
	t.Cleanup(func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	})

	tests := []struct {
		name        string
		response    []byte
		errCall     error
		wantMessage string
		wantLevel   string
	}{
		{
			name:        "unknown method RPC error",
			response:    marshalRPCError("unknown_method", "plugin.quiesce is unavailable"),
			wantMessage: "pluginhost: plugin quiesce unsupported",
			wantLevel:   "level=debug",
		},
		{
			name:        "standard unsupported indication",
			errCall:     fmt.Errorf("method not found: plugin.quiesce"),
			wantMessage: "pluginhost: plugin quiesce unsupported",
			wantLevel:   "level=debug",
		},
		{
			name:        "runtime failure",
			errCall:     fmt.Errorf("quiesce runtime failure"),
			wantMessage: "pluginhost: plugin quiesce failed",
			wantLevel:   "level=warning",
		},
		{
			name:        "context cancellation",
			errCall:     context.Canceled,
			wantMessage: "pluginhost: plugin quiesce canceled",
			wantLevel:   "level=debug",
		},
	}

	h := New()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out.Reset()
			client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
				if method != pluginabi.MethodPluginQuiesce {
					return nil, fmt.Errorf("unexpected plugin method %s", method)
				}
				return tt.response, tt.errCall
			}}
			if h.callQuiesce(context.Background(), &loadedPlugin{id: "alpha", client: client}) {
				t.Fatal("callQuiesce() = true, want false")
			}
			logs := out.String()
			if !strings.Contains(logs, tt.wantMessage) || !strings.Contains(logs, tt.wantLevel) {
				t.Fatalf("quiesce log = %q, want %q at %s", logs, tt.wantMessage, tt.wantLevel)
			}
		})
	}
}

func TestHostApplyConfigSerializesLifecycleDuringQuiesce(t *testing.T) {
	events := &lifecycleEventRecorder{}
	quiesceStarted := make(chan struct{})
	releaseQuiesce := make(chan struct{})
	replacementRegisterStarted := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseQuiesce) }) }
	t.Cleanup(release)

	var activeLifecycleCalls atomic.Int32
	var concurrentLifecycleCalls atomic.Bool
	enterLifecycle := func() func() {
		if activeLifecycleCalls.Add(1) > 1 {
			concurrentLifecycleCalls.Store(true)
		}
		return func() { activeLifecycleCalls.Add(-1) }
	}
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		exitLifecycle := enterLifecycle()
		defer exitLifecycle()
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			close(quiesceStarted)
			<-releaseQuiesce
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	var replacementRegisterOnce sync.Once
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		exitLifecycle := enterLifecycle()
		defer exitLifecycle()
		events.add("replacement." + method)
		switch method {
		case pluginabi.MethodPluginRegister:
			replacementRegisterOnce.Do(func() { close(replacementRegisterStarted) })
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginReconfigure:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		default:
			return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
		}
	}}
	loader := &sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")
	cfg := versionedPluginHostConfig(t, pluginsDir, "2.0.0")

	firstDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(firstDone)
	}()
	waitForHostTestSignal(t, quiesceStarted, "plugin quiesce")

	probeDone := make(chan struct{})
	go func() {
		_ = h.currentModelExecutor()
		close(probeDone)
	}()
	waitForHostTestSignal(t, probeDone, "Host.mu probe during quiesce")

	secondDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(secondDone)
	}()
	select {
	case <-replacementRegisterStarted:
		t.Fatal("replacement registration started before quiesce completed")
	case <-secondDone:
		t.Fatal("second ApplyConfig completed while quiesce was blocked")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	waitForHostTestSignal(t, firstDone, "first hot reload")
	waitForHostTestSignal(t, secondDone, "serialized reconfigure")
	if concurrentLifecycleCalls.Load() {
		t.Fatal("plugin lifecycle calls ran concurrently during quiesce")
	}
	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
		"replacement." + pluginabi.MethodPluginReconfigure,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
}

func TestHostApplyConfigRollsBackQuiescedPluginWhenContextCanceled(t *testing.T) {
	events := &lifecycleEventRecorder{}
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementRegisterStarted := make(chan struct{})
	replacementClient := &lifecycleTestClient{
		call: func(ctx context.Context, method string, _ []byte) ([]byte, error) {
			events.add("replacement." + method)
			if method != pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
			}
			close(replacementRegisterStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		},
		shutdown: func() { events.add("replacement.shutdown") },
	}
	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	cfg := versionedPluginHostConfig(t, pluginsDir, "2.0.0")
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(applyDone)
	}()
	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration")
	cancel()
	waitForHostTestSignal(t, applyDone, "canceled hot reload")

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
		"replacement.shutdown",
		"old." + pluginabi.MethodPluginReconfigure,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not remain active after canceled hot reload")
	}
	if replacementClient.shutdownCalls.Load() != 1 {
		t.Fatalf("replacement shutdown calls = %d, want 1", replacementClient.shutdownCalls.Load())
	}
}

func TestHostApplyConfigShutsDownSuccessfulReplacementBeforeCanceledRollback(t *testing.T) {
	events := &lifecycleEventRecorder{}
	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		events.add("old." + method)
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		default:
			return nil, fmt.Errorf("unexpected old plugin method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{
		call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			events.add("replacement." + method)
			if method != pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("unexpected replacement plugin method %s", method)
			}
			return lifecycleRegistrationResult(validTestPlugin("alpha"))
		},
		shutdown: func() { events.add("replacement.shutdown") },
	}
	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	ctx := &cancelOnErrContext{
		Context:  context.Background(),
		done:     make(chan struct{}),
		cancelAt: 3,
	}
	h.ApplyConfig(ctx, versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if got, want := events.snapshot(), []string{
		"old." + pluginabi.MethodPluginRegister,
		"old." + pluginabi.MethodPluginQuiesce,
		"replacement." + pluginabi.MethodPluginRegister,
		"replacement.shutdown",
		"old." + pluginabi.MethodPluginReconfigure,
	}; !slices.Equal(got, want) {
		t.Fatalf("lifecycle events = %v, want %v", got, want)
	}
	if replacementClient.shutdownCalls.Load() != 1 {
		t.Fatalf("replacement shutdown calls = %d, want 1", replacementClient.shutdownCalls.Load())
	}
	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("old plugin did not remain active after canceled replacement")
	}
}

func TestHostApplyConfigKeepsLoadedVersionWhenPinnedVersionMissing(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.4")

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": enabledPluginConfigWithStoreVersion(t, "1.0.4"),
			},
		},
	})
	if !h.pluginIdentityCurrent("alpha", paths["1.0.4"], "1.0.4") {
		t.Fatalf("active plugin identity did not start at %s", paths["1.0.4"])
	}

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": enabledPluginConfigWithStoreVersion(t, "1.0.5"),
			},
		},
	})
	if !h.PluginRegistered("alpha") {
		t.Fatal("PluginRegistered(alpha) = false, want old version to remain active while pinned version is missing")
	}
	if !h.pluginIdentityCurrent("alpha", paths["1.0.4"], "1.0.4") {
		t.Fatalf("active plugin identity changed before pinned version was available")
	}
	if loader.openCalls != 1 {
		t.Fatalf("Open calls = %d, want 1 while reusing loaded plugin", loader.openCalls)
	}
	if plugin.registerCalls != 1 || plugin.reconfigureCalls != 1 {
		t.Fatalf("calls = register %d reconfigure %d, want 1/1", plugin.registerCalls, plugin.reconfigureCalls)
	}

	paths["1.0.5"] = writeVersionedPluginFile(t, pluginsDir, "alpha", "1.0.5")
	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": enabledPluginConfigWithStoreVersion(t, "1.0.5"),
			},
		},
	})
	if !h.pluginIdentityCurrent("alpha", paths["1.0.5"], "1.0.5") {
		t.Fatalf("active plugin identity did not switch after pinned version was available")
	}
	if h.pluginIdentityCurrent("alpha", paths["1.0.4"], "1.0.4") {
		t.Fatal("old plugin identity is still active after pinned version became available")
	}
	if loader.openCalls != 2 {
		t.Fatalf("Open calls = %d, want 2 after loading pinned version", loader.openCalls)
	}
}

func TestHostApplyConfigLogsLoadedWhenRegistrationInvalid(t *testing.T) {
	var out bytes.Buffer
	originalOut := log.StandardLogger().Out
	originalFormatter := log.StandardLogger().Formatter
	originalLevel := log.GetLevel()
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	})

	loader := newTestSymbolLoader()
	loader.lookups["empty-name"] = newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin(""),
	})
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "empty-name"),
			Configs: enabledPluginConfigs("empty-name"),
		},
	})

	logs := out.String()
	if count := strings.Count(logs, `msg="pluginhost: plugin loaded"`); count != 1 {
		t.Fatalf("plugin loaded log count = %d, want 1\n%s", count, logs)
	}
	if strings.Contains(logs, `msg="pluginhost: plugin registered"`) {
		t.Fatalf("plugin registered log emitted for invalid registration:\n%s", logs)
	}
}

func TestRegisteredPluginsIncludesMetadataAndOAuthCapability(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	plugin.registerResult.Metadata.Logo = "https://example.com/logo.svg"
	plugin.registerResult.Metadata.ConfigFields = []pluginapi.ConfigField{{
		Name:        "mode",
		Type:        pluginapi.ConfigFieldTypeEnum,
		EnumValues:  []string{"safe", "fast"},
		Description: "Execution mode.",
	}}
	plugin.registerResult.Capabilities.AuthProvider = fakeAuthProvider{identifier: "alpha"}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	})

	infos := h.RegisteredPlugins()
	if len(infos) != 1 {
		t.Fatalf("RegisteredPlugins() len = %d, want 1; infos=%#v", len(infos), infos)
	}
	if !infos[0].SupportsOAuth {
		t.Fatalf("RegisteredPlugins()[0].SupportsOAuth = false, want true; infos=%#v", infos)
	}
	if infos[0].OAuthProvider != "alpha" {
		t.Fatalf("RegisteredPlugins()[0].OAuthProvider = %q, want alpha; infos=%#v", infos[0].OAuthProvider, infos)
	}
	if !reflect.DeepEqual(infos[0].OAuthProviders, []string{"alpha"}) {
		t.Fatalf("RegisteredPlugins()[0].OAuthProviders = %#v, want [alpha]; infos=%#v", infos[0].OAuthProviders, infos)
	}
	if infos[0].Metadata.Logo == "" || len(infos[0].Metadata.ConfigFields) != 1 {
		t.Fatalf("RegisteredPlugins()[0].Metadata = %#v, want logo and config fields", infos[0].Metadata)
	}
}

func TestRegisteredPluginsExposesMultipleOAuthProvidersViaRPC(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("zcode"),
		reconfigureResult: validTestPlugin("zcode"),
	}
	authProv := fakeAuthProvider{
		identifier:  "zcode-zai",
		identifiers: []string{"zcode-zai", "zcode-bigmodel"},
		startLogin: func(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
			return pluginapi.AuthLoginStartResponse{
				Provider: req.Provider,
				URL:      "https://login.example/" + req.Provider,
				State:    "state-" + req.Provider,
			}, nil
		},
	}
	plugin.registerResult.Capabilities.AuthProvider = authProv
	plugin.reconfigureResult.Capabilities.AuthProvider = authProv
	loader.lookups["zcode"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "zcode"),
			Configs: enabledPluginConfigs("zcode"),
		},
	})

	infos := h.RegisteredPlugins()
	if len(infos) != 1 {
		t.Fatalf("RegisteredPlugins() len = %d, want 1; infos=%#v", len(infos), infos)
	}
	if !infos[0].SupportsOAuth || infos[0].OAuthProvider != "zcode-zai" {
		t.Fatalf("RegisteredPlugins()[0] = %#v, want SupportsOAuth=true OAuthProvider=zcode-zai", infos[0])
	}
	if !reflect.DeepEqual(infos[0].OAuthProviders, []string{"zcode-zai", "zcode-bigmodel"}) {
		t.Fatalf("RegisteredPlugins()[0].OAuthProviders = %#v, want [zcode-zai zcode-bigmodel]", infos[0].OAuthProviders)
	}
	for _, portal := range []string{"zcode-zai", "zcode-bigmodel"} {
		resp, handled, errStart := h.StartLogin(context.Background(), portal, "http://127.0.0.1:8317/callback")
		if errStart != nil || !handled || resp.Provider != portal {
			t.Fatalf("StartLogin(%q) = (%#v, %v, %v), want handled", portal, resp, handled, errStart)
		}
	}
}

func TestHostApplyConfig_InvalidMetadataOrNoCapabilitiesSkipped(t *testing.T) {
	loader := newTestSymbolLoader()
	loader.lookups["empty-name"] = newTestSymbolLookup(&testPlugin{
		registerResult:    validTestPlugin(""),
		reconfigureResult: validTestPlugin(""),
	})
	loader.lookups["no-caps"] = newTestSymbolLookup(&testPlugin{
		registerResult:    validTestPlugin("no-caps"),
		reconfigureResult: validTestPlugin("no-caps"),
	})
	loader.lookups["no-caps"].registerOverride = func([]byte) pluginapi.Plugin {
		return pluginapi.Plugin{Metadata: pluginapi.Metadata{
			Name:             "no-caps",
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		}}
	}
	h := NewForTest(loader)

	h.ApplyConfig(context.Background(), &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "empty-name", "no-caps"),
		},
	})

	if len(h.activeRecords()) != 0 {
		t.Fatalf("Snapshot records = %d, want 0", len(h.activeRecords()))
	}
}

func TestHostApplyConfig_PanicFusesPluginForProcessLifetime(t *testing.T) {
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
		panicOnReload:     true,
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}

	h.ApplyConfig(context.Background(), cfg)
	h.ApplyConfig(context.Background(), cfg)
	plugin.panicOnReload = false
	h.ApplyConfig(context.Background(), cfg)

	if plugin.registerCalls != 1 {
		t.Fatalf("Register calls = %d, want 1", plugin.registerCalls)
	}
	if plugin.reconfigureCalls != 1 {
		t.Fatalf("Reconfigure calls = %d, want 1", plugin.reconfigureCalls)
	}
	if len(h.activeRecords()) != 0 {
		t.Fatalf("Snapshot records = %d, want 0 after fuse", len(h.activeRecords()))
	}
}

func TestHostApplyConfigDoesNotHoldHostMuDuringRegister(t *testing.T) {
	h, cfg, registerStarted, releaseRegister := newBlockingRegisterHost(t)
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(applyDone)
	}()

	waitForHostTestSignal(t, registerStarted, "register start")
	probeDone := make(chan struct{})
	go func() {
		_ = h.currentModelExecutor()
		close(probeDone)
	}()
	waitForHostTestSignal(t, probeDone, "Host.mu probe")

	releaseRegister()
	waitForHostTestSignal(t, applyDone, "ApplyConfig completion")

	snap := h.Snapshot()
	if !snap.enabled || len(snap.records) != 1 || snap.records[0].id != "alpha" {
		t.Fatalf("Snapshot() = %+v, want alpha registered", snap)
	}
}

func TestHostApplyConfigSerializesLifecycleCalls(t *testing.T) {
	loader := newTestSymbolLoader()
	started := make(chan struct{})
	release := make(chan struct{})
	secondEntered := make(chan struct{})
	var releaseOnce sync.Once
	releaseFirst := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFirst)

	var startOnce sync.Once
	var secondOnce sync.Once
	var lifecycleCalls int32
	var activeLifecycleCalls int32
	var concurrentLifecycleCalls int32
	lifecycle := func([]byte) pluginapi.Plugin {
		if active := atomic.AddInt32(&activeLifecycleCalls, 1); active > 1 {
			atomic.StoreInt32(&concurrentLifecycleCalls, 1)
		}
		call := atomic.AddInt32(&lifecycleCalls, 1)
		if call == 1 {
			startOnce.Do(func() { close(started) })
			<-release
		} else {
			secondOnce.Do(func() { close(secondEntered) })
		}
		atomic.AddInt32(&activeLifecycleCalls, -1)
		return validTestPlugin("alpha")
	}
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	lookup := newTestSymbolLookup(plugin)
	lookup.registerOverride = lifecycle
	lookup.reconfigureOverride = lifecycle
	loader.lookups["alpha"] = lookup
	h := NewForTest(loader)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}

	firstDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(firstDone)
	}()
	waitForHostTestSignal(t, started, "first register start")

	secondDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(secondDone)
	}()
	select {
	case <-secondEntered:
		t.Fatal("second ApplyConfig entered plugin lifecycle before first ApplyConfig finished")
	case <-time.After(200 * time.Millisecond):
	}

	releaseFirst()
	waitForHostTestSignal(t, firstDone, "first ApplyConfig completion")
	waitForHostTestSignal(t, secondDone, "second ApplyConfig completion")

	if got := atomic.LoadInt32(&lifecycleCalls); got != 2 {
		t.Fatalf("lifecycle calls = %d, want 2", got)
	}
	if atomic.LoadInt32(&concurrentLifecycleCalls) != 0 {
		t.Fatal("plugin lifecycle calls ran concurrently")
	}
}

func TestHostPluginBusyReportsLoadingPlugin(t *testing.T) {
	h, cfg, openStarted, releaseOpen := newBlockingOpenHost(t)
	t.Cleanup(h.ShutdownAll)

	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(applyDone)
	}()

	waitForHostTestSignal(t, openStarted, "plugin open start")
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false while plugin is still loading")
	}
	if !h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = false, want true while plugin is loading")
	}

	releaseOpen()
	waitForHostTestSignal(t, applyDone, "ApplyConfig completion")
	if !h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = false, want true after load")
	}
	if !h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = false, want true after load")
	}
}

func TestHostCanceledInitializationDiscardsBlockedClient(t *testing.T) {
	client := &blockingInitializationClient{
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		registration: validTestPlugin("alpha"),
	}
	h := NewForTest(&blockingHostCallLoader{client: client})
	cleanupDone := make(chan struct{})
	h.loadCleanupDoneHook = func() { close(cleanupDone) }
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(applyDone)
	}()
	waitForHostTestSignal(t, client.started, "plugin initialization")
	cancel()
	waitForHostTestSignal(t, applyDone, "canceled plugin initialization")
	if !h.PluginBusy("alpha") || h.PluginLoaded("alpha") {
		t.Fatal("canceled initialization did not retain only its in-flight load token")
	}

	close(client.release)
	// Late cleanup shuts the client down first and drops the load token after;
	// wait for the end of the cleanup, not for the shutdown alone.
	waitForHostTestSignal(t, cleanupDone, "late load cleanup")
	if got := client.shutdown.Load(); got != 1 {
		t.Fatalf("blocked initialization client shutdown calls = %d, want 1", got)
	}
	if h.PluginBusy("alpha") || h.PluginLoaded("alpha") {
		t.Fatal("canceled initialization remained in the host after late cleanup")
	}
}

func TestHostCancellationUnderMutationLockDoesNotInsertLoadedPlugin(t *testing.T) {
	client := &blockingInitializationClient{
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		completed:    make(chan struct{}),
		registration: validTestPlugin("alpha"),
	}
	h := NewForTest(&blockingHostCallLoader{client: client})
	cleanupDone := make(chan struct{})
	h.loadCleanupDoneHook = func() { close(cleanupDone) }
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(applyDone)
	}()
	waitForHostTestSignal(t, client.started, "plugin initialization")

	h.mu.Lock()
	close(client.release)
	waitForHostTestSignal(t, client.completed, "plugin initialization completion")
	cancel()
	h.mu.Unlock()
	waitForHostTestSignal(t, applyDone, "canceled plugin apply")

	// Late cleanup shuts the client down first and drops the load token after;
	// wait for the end of the cleanup, not for the shutdown alone.
	waitForHostTestSignal(t, cleanupDone, "late load cleanup")
	if got := client.shutdown.Load(); got != 1 {
		t.Fatalf("late client shutdown calls = %d, want 1", got)
	}
	if h.PluginLoaded("alpha") || h.PluginBusy("alpha") {
		t.Fatal("canceled load inserted or retained a completed plugin")
	}
}

func TestHostCanceledLoadDiscardsLateClientWithoutReplacingCurrentPlugin(t *testing.T) {
	first := &lateLoadClient{registration: validTestPlugin("alpha")}
	second := &lateLoadClient{registration: validTestPlugin("alpha")}
	loader := &lateLoadPluginLoader{
		first:         first,
		second:        second,
		firstStarted:  make(chan struct{}),
		firstRelease:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	h := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(firstDone)
	}()
	waitForHostTestSignal(t, loader.firstStarted, "first plugin load")
	cancel()
	waitForHostTestSignal(t, firstDone, "canceled plugin load")
	if !h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = false after canceled load, want retained load token")
	}

	secondDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(secondDone)
	}()
	waitForHostTestSignal(t, secondDone, "replacement apply completion")
	if got := loader.calls.Load(); got != 1 {
		t.Fatalf("Open calls = %d, want 1 while canceled load is still blocked", got)
	}
	select {
	case <-loader.secondStarted:
		t.Fatal("replacement started a second load before the canceled load completed")
	default:
	}

	close(loader.firstRelease)
	deadline := time.Now().Add(time.Second)
	for first.shutdown.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := first.shutdown.Load(); got != 1 {
		t.Fatalf("late client shutdown calls = %d, want 1", got)
	}
	if h.PluginBusy("alpha") || h.PluginLoaded("alpha") {
		t.Fatal("late canceled client remained in the host")
	}
	h.ShutdownAll()
}

func TestHostCanceledBlockedLoadKeepsOneLoaderAndCleanupPerPlugin(t *testing.T) {
	first := &lateLoadClient{registration: validTestPlugin("alpha")}
	loader := &lateLoadPluginLoader{
		first:         first,
		second:        &lateLoadClient{registration: validTestPlugin("alpha")},
		firstStarted:  make(chan struct{}),
		firstRelease:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	h := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(firstDone)
	}()
	waitForHostTestSignal(t, loader.firstStarted, "first plugin load")
	cancel()
	waitForHostTestSignal(t, firstDone, "canceled plugin load")

	for range 8 {
		h.ApplyConfig(context.Background(), cfg)
	}
	if got := loader.calls.Load(); got != 1 {
		t.Fatalf("Open calls = %d, want one blocked loader", got)
	}

	close(loader.firstRelease)
	deadline := time.Now().Add(time.Second)
	for first.shutdown.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := first.shutdown.Load(); got != 1 {
		t.Fatalf("late client shutdown calls = %d, want one cleanup", got)
	}
	if h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = true after blocked load cleanup")
	}
}

func TestHostUnloadPluginContextDetachesBlockedCall(t *testing.T) {
	plugin := validTestPlugin("alpha")
	client := &blockingHostCallClient{started: make(chan struct{}), release: make(chan struct{}), registration: plugin}
	loader := &blockingHostCallLoader{client: client}
	h := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}
	h.ApplyConfig(context.Background(), cfg)

	h.mu.Lock()
	loaded := h.loaded["alpha"]
	h.mu.Unlock()
	if loaded == nil {
		t.Fatal("plugin did not load")
	}
	go func() { _, _ = loaded.client.Call(context.Background(), pluginabi.MethodUsageHandle, nil) }()
	waitForHostTestSignal(t, client.started, "blocked plugin call")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unloadDone := make(chan bool, 1)
	go func() { unloadDone <- h.UnloadPluginContext(ctx, "alpha") }()
	if ok := waitForHostTestBool(t, unloadDone, "contextual unload"); !ok {
		t.Fatal("UnloadPluginContext() = false, want true after detaching runtime")
	}
	if h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = true after contextual unload detached runtime")
	}
	if got := client.shutdown.Load(); got != 0 {
		t.Fatalf("shutdown calls before blocked plugin call exits = %d, want 0", got)
	}

	close(client.release)
	deadline := time.Now().Add(time.Second)
	for client.shutdown.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.shutdown.Load(); got != 1 {
		t.Fatalf("shutdown calls after blocked plugin call exits = %d, want 1", got)
	}
}

func TestHostUnloadWaitsForBlockingLoad(t *testing.T) {
	h, cfg, openStarted, releaseOpen := newBlockingOpenHost(t)
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), cfg)
		close(applyDone)
	}()
	waitForHostTestSignal(t, openStarted, "plugin open start")

	unloadDone := make(chan bool)
	go func() {
		unloadDone <- h.UnloadPlugin("alpha")
	}()
	select {
	case <-unloadDone:
		t.Fatal("UnloadPlugin completed while ApplyConfig was still loading")
	case <-time.After(200 * time.Millisecond):
	}

	releaseOpen()
	waitForHostTestSignal(t, applyDone, "ApplyConfig completion")
	if ok := waitForHostTestBool(t, unloadDone, "UnloadPlugin completion"); !ok {
		t.Fatal("UnloadPlugin returned false, want true after loading completes")
	}
	if h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = true, want false after unload")
	}
}

func TestHostUnloadAndShutdownWaitForBlockingRegister(t *testing.T) {
	tests := []struct {
		name       string
		action     func(*Host) bool
		assertDone func(*testing.T, *Host)
	}{
		{
			name: "unload",
			action: func(h *Host) bool {
				return h.UnloadPlugin("alpha")
			},
			assertDone: func(t *testing.T, h *Host) {
				t.Helper()
				if h.PluginLoaded("alpha") {
					t.Fatal("PluginLoaded(alpha) = true, want false after unload")
				}
			},
		},
		{
			name: "shutdown",
			action: func(h *Host) bool {
				h.ShutdownAll()
				return true
			},
			assertDone: func(t *testing.T, h *Host) {
				t.Helper()
				if h.PluginLoaded("alpha") {
					t.Fatal("PluginLoaded(alpha) = true, want false after shutdown")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, cfg, registerStarted, releaseRegister := newBlockingRegisterHost(t)
			applyDone := make(chan struct{})
			go func() {
				h.ApplyConfig(context.Background(), cfg)
				close(applyDone)
			}()
			waitForHostTestSignal(t, registerStarted, "register start")

			actionDone := make(chan bool)
			go func() {
				actionDone <- tt.action(h)
			}()
			select {
			case <-actionDone:
				t.Fatalf("%s completed while ApplyConfig was still registering", tt.name)
			case <-time.After(200 * time.Millisecond):
			}

			releaseRegister()
			waitForHostTestSignal(t, applyDone, "ApplyConfig completion")
			if ok := waitForHostTestBool(t, actionDone, tt.name+" completion"); !ok {
				t.Fatalf("%s returned false, want true", tt.name)
			}
			tt.assertDone(t, h)
		})
	}
}

func TestSortRecordsPriorityDescendingAndIDTieBreak(t *testing.T) {
	records := []capabilityRecord{
		{id: "charlie", priority: 1},
		{id: "bravo", priority: 2},
		{id: "alpha", priority: 2},
	}

	sortRecords(records)

	want := []string{"alpha", "bravo", "charlie"}
	for index, id := range want {
		if records[index].id != id {
			t.Fatalf("records[%d].id = %q, want %q", index, records[index].id, id)
		}
	}
}

type lifecycleEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *lifecycleEventRecorder) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *lifecycleEventRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type lifecycleTestClient struct {
	call          func(context.Context, string, []byte) ([]byte, error)
	shutdown      func()
	shutdownCalls atomic.Int32
}

func (c *lifecycleTestClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c == nil || c.call == nil {
		return nil, fmt.Errorf("lifecycle test client has no call handler")
	}
	return c.call(ctx, method, request)
}

func (c *lifecycleTestClient) Shutdown() {
	c.shutdownCalls.Add(1)
	if c.shutdown != nil {
		c.shutdown()
	}
}

type cancelOnErrContext struct {
	context.Context
	done     chan struct{}
	cancelAt int32
	errCalls atomic.Int32
	cancel   sync.Once
}

type errObserveContext struct {
	context.Context
	onCanceledErr func()
}

func (c *errObserveContext) Err() error {
	err := c.Context.Err()
	if err != nil && c.onCanceledErr != nil {
		c.onCanceledErr()
	}
	return err
}

func (c *cancelOnErrContext) Done() <-chan struct{} {
	return c.done
}

func (c *cancelOnErrContext) Err() error {
	if c.errCalls.Add(1) < c.cancelAt {
		return nil
	}
	c.cancel.Do(func() { close(c.done) })
	return context.Canceled
}

type sequencePluginLoader struct {
	mu      sync.Mutex
	clients []pluginClient
	calls   int
}

func (l *sequencePluginLoader) Open(file pluginFile, _ *Host) (pluginClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls >= len(l.clients) {
		return nil, fmt.Errorf("missing test client for %s", file.Path)
	}
	client := l.clients[l.calls]
	l.calls++
	return client, nil
}

func lifecycleRegistrationResult(plugin pluginapi.Plugin) ([]byte, error) {
	return marshalRPCResult(rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      plugin.Metadata,
		Capabilities:  rpcCapabilitiesFromPlugin(plugin),
	})
}

func versionedPluginHostConfig(t *testing.T, pluginsDir string, version string) *config.Config {
	t.Helper()
	return &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     pluginsDir,
		Configs: map[string]config.PluginInstanceConfig{
			"alpha": enabledPluginConfigWithStoreVersion(t, version),
		},
	}}
}

type capturePluginClient struct {
	requests map[string][]byte
}

func (c *capturePluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if c.requests == nil {
		c.requests = make(map[string][]byte)
	}
	c.requests[method] = append([]byte(nil), request...)
	return marshalRPCResult(rpcEmptyResponse{})
}

func (c *capturePluginClient) Shutdown() {}

type blockingInitializationClient struct {
	started         chan struct{}
	release         chan struct{}
	completed       chan struct{}
	registration    pluginapi.Plugin
	shutdown        atomic.Int32
	shutdownStarted chan struct{}
	shutdownRelease chan struct{}
}

func (c *blockingInitializationClient) Call(_ context.Context, method string, _ []byte) ([]byte, error) {
	if method != pluginabi.MethodPluginRegister {
		return nil, fmt.Errorf("unexpected plugin method %s", method)
	}
	close(c.started)
	<-c.release
	if c.completed != nil {
		close(c.completed)
	}
	return marshalRPCResult(rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      c.registration.Metadata,
		Capabilities:  rpcCapabilitiesFromPlugin(c.registration),
	})
}

func (c *blockingInitializationClient) Shutdown() {
	c.shutdown.Add(1)
	if c.shutdownStarted != nil {
		close(c.shutdownStarted)
	}
	if c.shutdownRelease != nil {
		<-c.shutdownRelease
	}
}

type lateLoadPluginLoader struct {
	first         pluginClient
	second        pluginClient
	firstStarted  chan struct{}
	firstRelease  chan struct{}
	secondStarted chan struct{}
	calls         atomic.Int32
}

func (l *lateLoadPluginLoader) Open(pluginFile, *Host) (pluginClient, error) {
	if l.calls.Add(1) == 1 {
		close(l.firstStarted)
		<-l.firstRelease
		return l.first, nil
	}
	close(l.secondStarted)
	return l.second, nil
}

type lateLoadClient struct {
	registration pluginapi.Plugin
	shutdown     atomic.Int32
}

func (c *lateLoadClient) Call(_ context.Context, method string, _ []byte) ([]byte, error) {
	if method != pluginabi.MethodPluginRegister {
		return nil, fmt.Errorf("unexpected plugin method %s", method)
	}
	return marshalRPCResult(rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      c.registration.Metadata,
		Capabilities:  rpcCapabilitiesFromPlugin(c.registration),
	})
}

func (c *lateLoadClient) Shutdown() {
	c.shutdown.Add(1)
}

type blockingHostCallLoader struct {
	client pluginClient
}

func (l *blockingHostCallLoader) Open(pluginFile, *Host) (pluginClient, error) {
	return l.client, nil
}

type blockingHostCallClient struct {
	started      chan struct{}
	release      chan struct{}
	registration pluginapi.Plugin
	shutdown     atomic.Int32
}

func (c *blockingHostCallClient) Call(_ context.Context, method string, _ []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		return marshalRPCResult(rpcRegistration{
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata:      c.registration.Metadata,
			Capabilities:  rpcCapabilitiesFromPlugin(c.registration),
		})
	case pluginabi.MethodUsageHandle:
		close(c.started)
		<-c.release
		return marshalRPCResult(rpcEmptyResponse{})
	default:
		return nil, fmt.Errorf("unexpected plugin method %s", method)
	}
}

func (c *blockingHostCallClient) Shutdown() {
	c.shutdown.Add(1)
}

type blockingOpenLoader struct {
	inner     *testSymbolLoader
	started   chan struct{}
	release   <-chan struct{}
	startOnce sync.Once
}

func (l *blockingOpenLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	l.startOnce.Do(func() { close(l.started) })
	<-l.release
	return l.inner.Open(file, host)
}

func newBlockingOpenHost(t *testing.T) (*Host, *config.Config, <-chan struct{}, func()) {
	t.Helper()

	inner := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	inner.lookups["alpha"] = newTestSymbolLookup(plugin)

	openStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseOpen := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseOpen)

	h := NewForTest(&blockingOpenLoader{
		inner:   inner,
		started: openStarted,
		release: release,
	})
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}
	return h, cfg, openStarted, releaseOpen
}

func newBlockingRegisterHost(t *testing.T) (*Host, *config.Config, <-chan struct{}, func()) {
	t.Helper()

	loader := newTestSymbolLoader()
	registerStarted := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	releaseRegister := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseRegister)

	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	lookup := newTestSymbolLookup(plugin)
	lookup.registerOverride = func([]byte) pluginapi.Plugin {
		startOnce.Do(func() { close(registerStarted) })
		<-release
		return validTestPlugin("alpha")
	}
	loader.lookups["alpha"] = lookup
	h := NewForTest(loader)
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Dir:     makePluginDir(t, "alpha"),
			Configs: enabledPluginConfigs("alpha"),
		},
	}
	return h, cfg, registerStarted, releaseRegister
}

func waitForHostTestSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForHostTestBool(t *testing.T, ch <-chan bool, name string) bool {
	t.Helper()
	select {
	case ok := <-ch:
		return ok
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return false
	}
}

type countingPluginLoader struct {
	client      pluginClient
	replacement pluginClient
	calls       atomic.Int32
}

func (l *countingPluginLoader) Open(pluginFile, *Host) (pluginClient, error) {
	if l.calls.Add(1) == 1 {
		return l.client, nil
	}
	return l.replacement, nil
}

type callbackInstanceCleanupClient struct {
	shutdown chan struct{}
	once     sync.Once
}

func (c *callbackInstanceCleanupClient) Call(context.Context, string, []byte) ([]byte, error) {
	return nil, nil
}

func (c *callbackInstanceCleanupClient) Shutdown() {
	c.once.Do(func() { close(c.shutdown) })
}

func finishPendingPluginLoadForTest(t *testing.T, request *pluginLoadRequest, instance *hostCallbackInstance) {
	t.Helper()
	client := &callbackInstanceCleanupClient{shutdown: make(chan struct{})}
	request.result <- pluginLoadResult{loaded: &loadedPlugin{id: "alpha", client: client, callbackInstance: instance}}
	waitForHostTestSignal(t, client.shutdown, "pending plugin client shutdown")
}

func TestHostCanceledLoadRejectsLateCallbackInstance(t *testing.T) {
	h := New()
	request := &pluginLoadRequest{result: make(chan pluginLoadResult, 1)}
	h.mu.Lock()
	h.loading["alpha"] = request
	h.mu.Unlock()
	var instance *hostCallbackInstance
	defer func() { finishPendingPluginLoadForTest(t, request, instance) }()

	h.cleanupCanceledPluginLoad("alpha", request)
	instance = &hostCallbackInstance{}
	h.registerHostCallbackInstance("alpha", instance)
	ctx := withHostCallbackIdentity(context.Background(), "alpha", instance)
	if _, errOpen := h.openHostHTTPOperation(ctx, ""); errOpen == nil {
		t.Fatal("canceled load allowed a late callback instance to open an HTTP operation")
	}
}

func TestHostShutdownAllRejectsLateCallbackInstanceFromPendingLoad(t *testing.T) {
	h := New()
	request := &pluginLoadRequest{result: make(chan pluginLoadResult, 1)}
	h.mu.Lock()
	h.loading["alpha"] = request
	h.mu.Unlock()
	var instance *hostCallbackInstance
	defer func() { finishPendingPluginLoadForTest(t, request, instance) }()

	h.ShutdownAll()
	instance = &hostCallbackInstance{}
	h.registerHostCallbackInstance("alpha", instance)
	ctx := withHostCallbackIdentity(context.Background(), "alpha", instance)
	if _, errOpen := h.openHostHTTPOperation(ctx, ""); errOpen == nil {
		t.Fatal("shutdown allowed a late callback instance to open an HTTP operation")
	}
}

func TestHostUnloadRejectsLateCallbackInstanceFromPendingLoad(t *testing.T) {
	h := New()
	request := &pluginLoadRequest{result: make(chan pluginLoadResult, 1)}
	h.mu.Lock()
	h.loading["alpha"] = request
	h.mu.Unlock()
	var instance *hostCallbackInstance
	defer func() { finishPendingPluginLoadForTest(t, request, instance) }()

	if !h.UnloadPlugin("alpha") {
		t.Fatal("UnloadPlugin(alpha) = false, want true for a pending load")
	}
	instance = &hostCallbackInstance{}
	h.registerHostCallbackInstance("alpha", instance)
	ctx := withHostCallbackIdentity(context.Background(), "alpha", instance)
	if _, errOpen := h.openHostHTTPOperation(ctx, ""); errOpen == nil {
		t.Fatal("unload allowed a late callback instance to open an HTTP operation")
	}
}

func TestHostShutdownAllRetainsBlockedLoadTokenUntilCleanup(t *testing.T) {
	client := &blockingInitializationClient{
		started:         make(chan struct{}),
		release:         make(chan struct{}),
		registration:    validTestPlugin("alpha"),
		shutdownStarted: make(chan struct{}),
		shutdownRelease: make(chan struct{}),
	}
	loader := &countingPluginLoader{client: client, replacement: &lateLoadClient{registration: validTestPlugin("alpha")}}
	h := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(firstDone)
	}()
	waitForHostTestSignal(t, client.started, "plugin registration")
	cancel()
	waitForHostTestSignal(t, firstDone, "canceled plugin apply")
	close(client.release)
	waitForHostTestSignal(t, client.shutdownStarted, "plugin shutdown")

	h.ShutdownAllContext(context.Background())
	var applies sync.WaitGroup
	for range 8 {
		applies.Add(1)
		go func() {
			defer applies.Done()
			h.ApplyConfig(context.Background(), cfg)
		}()
	}
	applies.Wait()
	if got := loader.calls.Load(); got != 1 {
		t.Fatalf("Open calls while ShutdownAll cleanup is blocked = %d, want 1", got)
	}
	if !h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = false before physical shutdown returns")
	}

	close(client.shutdownRelease)
	deadline := time.Now().Add(time.Second)
	for h.PluginBusy("alpha") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = true after physical shutdown returned")
	}
}

func TestHostCanceledRegisterRetainsLoadTokenUntilShutdownReturns(t *testing.T) {
	client := &blockingInitializationClient{
		started:         make(chan struct{}),
		release:         make(chan struct{}),
		registration:    validTestPlugin("alpha"),
		shutdownStarted: make(chan struct{}),
		shutdownRelease: make(chan struct{}),
	}
	loader := &countingPluginLoader{client: client, replacement: &lateLoadClient{registration: validTestPlugin("alpha")}}
	h := NewForTest(loader)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "alpha"),
		Configs: enabledPluginConfigs("alpha"),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(ctx, cfg)
		close(applyDone)
	}()
	waitForHostTestSignal(t, client.started, "plugin registration")
	cancel()
	waitForHostTestSignal(t, applyDone, "canceled plugin apply")
	close(client.release)
	waitForHostTestSignal(t, client.shutdownStarted, "plugin shutdown")

	for range 8 {
		h.ApplyConfig(context.Background(), cfg)
	}
	if got := loader.calls.Load(); got != 1 {
		t.Fatalf("Open calls while shutdown is blocked = %d, want 1", got)
	}
	if !h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = false before physical shutdown returns")
	}

	close(client.shutdownRelease)
	deadline := time.Now().Add(time.Second)
	for h.PluginBusy("alpha") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if h.PluginBusy("alpha") {
		t.Fatal("PluginBusy(alpha) = true after physical shutdown returned")
	}
}
