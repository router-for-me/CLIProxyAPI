package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testSymbolLoader struct {
	openCalls int
	lookups   map[string]*testSymbolLookup
}

func newTestSymbolLoader() *testSymbolLoader {
	return &testSymbolLoader{lookups: make(map[string]*testSymbolLookup)}
}

func (l *testSymbolLoader) Open(path string, host *Host) (pluginClient, error) {
	l.openCalls++
	lookup := l.lookups[pluginIDFromPath(path)]
	if lookup == nil {
		return nil, fmt.Errorf("missing test plugin for %s", path)
	}
	return lookup, nil
}

type testSymbolLookup struct {
	plugin              *testPlugin
	active              pluginapi.Plugin
	registerOverride    func([]byte) pluginapi.Plugin
	reconfigureOverride func([]byte) pluginapi.Plugin
}

func newTestSymbolLookup(plugin *testPlugin) *testSymbolLookup {
	return &testSymbolLookup{plugin: plugin}
}

func (l *testSymbolLookup) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister:
		return l.callLifecycle(request, false)
	case pluginabi.MethodPluginReconfigure:
		return l.callLifecycle(request, true)
	case pluginabi.MethodThinkingIdentifier:
		if l.active.Capabilities.ThinkingApplier == nil {
			return nil, fmt.Errorf("missing thinking applier")
		}
		return marshalRPCResult(rpcIdentifierResponse{Identifier: l.active.Capabilities.ThinkingApplier.Identifier()})
	case pluginabi.MethodThinkingApply:
		var req pluginapi.ThinkingApplyRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		resp, errApply := l.active.Capabilities.ThinkingApplier.ApplyThinking(ctx, req)
		if errApply != nil {
			return nil, errApply
		}
		return marshalRPCResult(resp)
	case pluginabi.MethodRequestInterceptBefore:
		if l.active.Capabilities.RequestInterceptor == nil {
			return nil, fmt.Errorf("missing request interceptor")
		}
		var req pluginapi.RequestInterceptRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		resp, errIntercept := l.active.Capabilities.RequestInterceptor.InterceptRequest(ctx, req)
		if errIntercept != nil {
			return nil, errIntercept
		}
		return marshalRPCResult(resp)
	case pluginabi.MethodResponseInterceptAfter:
		if l.active.Capabilities.ResponseInterceptor == nil {
			return nil, fmt.Errorf("missing response interceptor")
		}
		var req pluginapi.ResponseInterceptRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		resp, errIntercept := l.active.Capabilities.ResponseInterceptor.InterceptResponse(ctx, req)
		if errIntercept != nil {
			return nil, errIntercept
		}
		return marshalRPCResult(resp)
	case pluginabi.MethodResponseInterceptStreamChunk:
		if l.active.Capabilities.StreamChunkInterceptor == nil {
			return nil, fmt.Errorf("missing stream chunk interceptor")
		}
		var req pluginapi.StreamChunkInterceptRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		resp, errIntercept := l.active.Capabilities.StreamChunkInterceptor.InterceptStreamChunk(ctx, req)
		if errIntercept != nil {
			return nil, errIntercept
		}
		return marshalRPCResult(resp)
	case pluginabi.MethodAuthIdentifier:
		if l.active.Capabilities.AuthProvider == nil {
			return nil, fmt.Errorf("missing auth provider")
		}
		return marshalRPCResult(rpcIdentifierResponse{Identifier: l.active.Capabilities.AuthProvider.Identifier()})
	case pluginabi.MethodUsageHandle:
		if l.active.Capabilities.UsagePlugin == nil {
			return marshalRPCResult(rpcEmptyResponse{})
		}
		var record pluginapi.UsageRecord
		if errUnmarshal := json.Unmarshal(request, &record); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		l.active.Capabilities.UsagePlugin.HandleUsage(ctx, record)
		return marshalRPCResult(rpcEmptyResponse{})
	default:
		return nil, fmt.Errorf("missing test method %s", method)
	}
}

func (l *testSymbolLookup) Shutdown() {}

func (l *testSymbolLookup) callLifecycle(request []byte, reload bool) ([]byte, error) {
	var req rpcLifecycleRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	var plugin pluginapi.Plugin
	if reload {
		if l.reconfigureOverride != nil {
			plugin = l.reconfigureOverride(req.ConfigYAML)
		} else {
			plugin = l.plugin.Reconfigure(req.ConfigYAML)
		}
	} else {
		if l.registerOverride != nil {
			plugin = l.registerOverride(req.ConfigYAML)
		} else {
			plugin = l.plugin.Register(req.ConfigYAML)
		}
	}
	l.active = plugin
	return marshalRPCResult(rpcRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      plugin.Metadata,
		Capabilities:  rpcCapabilitiesFromPlugin(plugin),
	})
}

type testPlugin struct {
	registerCalls     int
	reconfigureCalls  int
	registerResult    pluginapi.Plugin
	reconfigureResult pluginapi.Plugin
	panicOnRegister   bool
	panicOnReload     bool
}

func (p *testPlugin) Register([]byte) pluginapi.Plugin {
	p.registerCalls++
	if p.panicOnRegister {
		panic("register panic")
	}
	return p.registerResult
}

func (p *testPlugin) Reconfigure([]byte) pluginapi.Plugin {
	p.reconfigureCalls++
	if p.panicOnReload {
		panic("reconfigure panic")
	}
	return p.reconfigureResult
}

func validTestPlugin(name string) pluginapi.Plugin {
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             name,
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		},
		Capabilities: pluginapi.Capabilities{
			UsagePlugin: testUsageCapability{},
		},
	}
}

type testUsageCapability struct{}

func (testUsageCapability) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {}

type testThinkingCapability struct {
	provider string
}

func (c testThinkingCapability) Identifier() string {
	return c.provider
}

func (c testThinkingCapability) ApplyThinking(ctx context.Context, req pluginapi.ThinkingApplyRequest) (pluginapi.PayloadResponse, error) {
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
		return pluginapi.PayloadResponse{}, errUnmarshal
	}
	payload["plugin"] = c.provider
	payload["thinking_budget"] = req.Config.Budget
	out, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return pluginapi.PayloadResponse{}, errMarshal
	}
	return pluginapi.PayloadResponse{Body: out}, nil
}

type requestInterceptorFunc func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error)

func (f requestInterceptorFunc) InterceptRequest(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if f == nil {
		return pluginapi.RequestInterceptResponse{}, fmt.Errorf("missing request interceptor callback")
	}
	return f(ctx, req)
}

type responseInterceptorFunc struct {
	interceptResponse    func(context.Context, pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error)
	interceptStreamChunk func(context.Context, pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error)
}

func (f responseInterceptorFunc) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
	if f.interceptResponse == nil {
		return pluginapi.ResponseInterceptResponse{}, fmt.Errorf("missing response interceptor callback")
	}
	return f.interceptResponse(ctx, req)
}

func (f responseInterceptorFunc) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
	if f.interceptStreamChunk == nil {
		return pluginapi.StreamChunkInterceptResponse{}, fmt.Errorf("missing stream chunk interceptor callback")
	}
	return f.interceptStreamChunk(ctx, req)
}

func makePluginDir(t *testing.T, ids ...string) string {
	t.Helper()
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}
	for _, id := range ids {
		path := filepath.Join(archDir, id+pluginExtension(runtime.GOOS))
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}
	return root
}
