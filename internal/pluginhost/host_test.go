package pluginhost

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

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
	if len(h.Snapshot().records) != 0 {
		t.Fatalf("Snapshot records = %d, want 0", len(h.Snapshot().records))
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
		},
	})

	if len(h.Snapshot().records) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.Snapshot().records))
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
		},
	})

	if len(h.Snapshot().records) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.Snapshot().records))
	}

	caps := h.Snapshot().records[0].plugin.Capabilities
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
	if len(h.Snapshot().records) != 1 {
		t.Fatalf("Snapshot records = %d, want 1", len(h.Snapshot().records))
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
		},
	})

	infos := h.RegisteredPlugins()
	if len(infos) != 1 {
		t.Fatalf("RegisteredPlugins() len = %d, want 1; infos=%#v", len(infos), infos)
	}
	if !infos[0].SupportsOAuth {
		t.Fatalf("RegisteredPlugins()[0].SupportsOAuth = false, want true; infos=%#v", infos)
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

	if len(h.Snapshot().records) != 0 {
		t.Fatalf("Snapshot records = %d, want 0", len(h.Snapshot().records))
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
	if len(h.Snapshot().records) != 0 {
		t.Fatalf("Snapshot records = %d, want 0 after fuse", len(h.Snapshot().records))
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
