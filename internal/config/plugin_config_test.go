package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytes_PluginsDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
plugins: {}
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if cfg.Plugins.Enabled {
		t.Fatal("Plugins.Enabled = true, want false")
	}
	if cfg.Plugins.Dir != "plugins" {
		t.Fatalf("Plugins.Dir = %q, want plugins", cfg.Plugins.Dir)
	}
	if cfg.Plugins.Configs == nil {
		t.Fatal("Plugins.Configs = nil, want empty map")
	}
	if len(cfg.Plugins.Configs) != 0 {
		t.Fatalf("len(Plugins.Configs) = %d, want 0", len(cfg.Plugins.Configs))
	}
}

func TestParseConfigBytes_PluginStoreSources(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
plugins:
  store-sources:
    - " https://community.example/registry.json "
    - ""
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.Plugins.StoreSources) != 1 {
		t.Fatalf("Plugins.StoreSources len = %d, want 1", len(cfg.Plugins.StoreSources))
	}
	source := cfg.Plugins.StoreSources[0]
	if source != "https://community.example/registry.json" {
		t.Fatalf("Plugins.StoreSources[0] = %#v", source)
	}
}

func TestParseConfigBytes_PluginInstanceEmptyRawYAML(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
plugins:
  configs:
    sample: {}
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	plugin, ok := cfg.Plugins.Configs["sample"]
	if !ok {
		t.Fatal("Plugins.Configs[\"sample\"] missing")
	}
	if plugin.Enabled == nil {
		t.Fatal("Plugin.Enabled = nil, want true pointer")
	}
	if !*plugin.Enabled {
		t.Fatal("Plugin.Enabled = false, want true")
	}
	if plugin.Priority != 0 {
		t.Fatalf("Plugin.Priority = %d, want 0", plugin.Priority)
	}

	raw, errMarshal := yaml.Marshal(&plugin.Raw)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal(Raw) error = %v", errMarshal)
	}
	rawText := string(raw)
	if strings.Contains(rawText, "enabled:") {
		t.Fatalf("Raw YAML contains enabled default:\n%s", rawText)
	}
	if strings.Contains(rawText, "priority:") {
		t.Fatalf("Raw YAML contains priority default:\n%s", rawText)
	}

	marshaled, errMarshalPlugin := yaml.Marshal(plugin)
	if errMarshalPlugin != nil {
		t.Fatalf("yaml.Marshal(plugin) error = %v", errMarshalPlugin)
	}
	marshaledText := string(marshaled)
	if strings.Contains(marshaledText, "enabled:") {
		t.Fatalf("Plugin YAML contains enabled default:\n%s", marshaledText)
	}
	if strings.Contains(marshaledText, "priority:") {
		t.Fatalf("Plugin YAML contains priority default:\n%s", marshaledText)
	}
}

func TestSaveConfigPreserveComments_PrunesDefaultPluginsDir(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("debug: true\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		Debug: true,
		Plugins: PluginsConfig{
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{},
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	data, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	text := string(data)
	if strings.Contains(text, "plugins:") {
		t.Fatalf("saved config contains plugins default section:\n%s", text)
	}
	if strings.Contains(text, "dir: plugins") {
		t.Fatalf("saved config contains default plugins dir:\n%s", text)
	}
}

func TestParseConfigBytes_PluginInstanceRawYAML(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
plugins:
  enabled: true
  dir: custom-plugins
  configs:
    sample:
      enabled: false
      priority: 7
      config1: value1
      config2:
        nested: value2
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	plugin, ok := cfg.Plugins.Configs["sample"]
	if !ok {
		t.Fatal("Plugins.Configs[\"sample\"] missing")
	}
	if plugin.Enabled == nil {
		t.Fatal("Plugin.Enabled = nil, want false pointer")
	}
	if *plugin.Enabled {
		t.Fatal("Plugin.Enabled = true, want false")
	}
	if plugin.Priority != 7 {
		t.Fatalf("Plugin.Priority = %d, want 7", plugin.Priority)
	}

	raw, errMarshal := yaml.Marshal(&plugin.Raw)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal(Raw) error = %v", errMarshal)
	}
	rawText := string(raw)
	for _, want := range []string{
		"enabled: false",
		"priority: 7",
		"config1: value1",
		"config2:",
		"nested: value2",
	} {
		if !strings.Contains(rawText, want) {
			t.Fatalf("Raw YAML missing %q in:\n%s", want, rawText)
		}
	}
}
