package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
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
	loader.lookups["no-caps"].symbols["Register"] = func([]byte) pluginapi.Plugin {
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
