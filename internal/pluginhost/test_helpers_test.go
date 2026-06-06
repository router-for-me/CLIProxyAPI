package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type testSymbolLoader struct {
	openCalls int
	lookups   map[string]*testSymbolLookup
}

func newTestSymbolLoader() *testSymbolLoader {
	return &testSymbolLoader{lookups: make(map[string]*testSymbolLookup)}
}

func (l *testSymbolLoader) Open(path string) (symbolLookup, error) {
	l.openCalls++
	lookup := l.lookups[pluginIDFromPath(path)]
	if lookup == nil {
		return nil, fmt.Errorf("missing test plugin for %s", path)
	}
	return lookup, nil
}

type testSymbolLookup struct {
	symbols map[string]any
}

func newTestSymbolLookup(plugin *testPlugin) *testSymbolLookup {
	return &testSymbolLookup{
		symbols: map[string]any{
			"Register":    plugin.Register,
			"Reconfigure": plugin.Reconfigure,
		},
	}
}

func (l *testSymbolLookup) Lookup(name string) (any, error) {
	symbol, ok := l.symbols[name]
	if !ok {
		return nil, fmt.Errorf("missing symbol %s", name)
	}
	return symbol, nil
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

func makePluginDir(t *testing.T, ids ...string) string {
	t.Helper()
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}
	for _, id := range ids {
		path := filepath.Join(archDir, id+".so")
		if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
		}
	}
	return root
}
