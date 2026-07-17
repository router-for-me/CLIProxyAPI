package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
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
	if snap := h.Snapshot(); snap.enabled || len(snap.records) != 0 {
		t.Fatalf("Snapshot() = %+v, want empty disabled snapshot", snap)
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

func TestPluginLoadedTracksLoadedPluginAfterDisabled(t *testing.T) {
	disabled := false
	loader := newTestSymbolLoader()
	plugin := &testPlugin{
		registerResult:    validTestPlugin("alpha"),
		reconfigureResult: validTestPlugin("alpha"),
	}
	loader.lookups["alpha"] = newTestSymbolLookup(plugin)
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
	if !h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = false, want true while library remains loaded")
	}

	h.ShutdownAll()
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after ShutdownAll")
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

	if !h.UnloadPlugin("alpha") {
		t.Fatal("UnloadPlugin(alpha) = false, want true")
	}
	if h.PluginLoaded("alpha") {
		t.Fatal("PluginLoaded(alpha) = true, want false after targeted unload")
	}
	if !h.PluginLoaded("bravo") {
		t.Fatal("PluginLoaded(bravo) = false, want true after alpha unload")
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
	if got := gjson.GetBytes(out, "thinking_budget").Int(); got != 10240 {
		t.Fatalf("thinking_budget = %d, want 10240; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "plugin").String(); got != "plugin-thinking" {
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
	if infos[0].Metadata.Logo == "" || len(infos[0].Metadata.ConfigFields) != 1 {
		t.Fatalf("RegisteredPlugins()[0].Metadata = %#v, want logo and config fields", infos[0].Metadata)
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
