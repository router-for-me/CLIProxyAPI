package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func parseTestPluginConfig(t *testing.T, yamlText string) PluginInstanceConfig {
	t.Helper()
	var item PluginInstanceConfig
	if errUnmarshal := yaml.Unmarshal([]byte(yamlText), &item); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errUnmarshal)
	}
	return item
}

func TestSaveConfigPreserveComments_PluginConfigPreservesZeroValuesAndBooleans(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `plugins:
  enabled: true
  configs:
    sample:
      name: initial
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	enabledFalse := false
	cfg := &Config{
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"sample": parseTestPluginConfig(t, `name: updated
enabled: false
timeout: 0
empty_str: ""
empty_list: []
`),
			},
		},
	}
	cfg.Plugins.Configs["sample"] = PluginInstanceConfig{
		Enabled: &enabledFalse,
		Raw:     cfg.Plugins.Configs["sample"].Raw,
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	// In the reported bug, newly added false booleans, zeros, empty strings and empty lists
	// are silently dropped by mergeMappingPreserve/isKnownDefaultValue.
	for _, expected := range []string{
		"enabled: false",
		"timeout: 0",
		"empty_list: []",
		"empty_str: \"\"",
	} {
		if !strings.Contains(savedText, expected) {
			t.Errorf("saved config missing %q; got:\n%s", expected, savedText)
		}
	}

	var loaded struct {
		Plugins struct {
			Configs map[string]map[string]any `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if errUnmarshal := yaml.Unmarshal(savedBytes, &loaded); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal(savedBytes) error = %v", errUnmarshal)
	}
	sampleConfig := loaded.Plugins.Configs["sample"]
	if sampleConfig["enabled"] != false {
		t.Errorf("loaded enabled = %#v, want false", sampleConfig["enabled"])
	}
	if sampleConfig["timeout"] != 0 {
		t.Errorf("loaded timeout = %#v, want 0", sampleConfig["timeout"])
	}
	if sampleConfig["empty_str"] != "" {
		t.Errorf("loaded empty_str = %#v, want empty string", sampleConfig["empty_str"])
	}
}

func TestSaveConfigPreserveComments_PluginConfigDeletesRemovedKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `plugins:
  enabled: true
  configs:
    sample:
      name: initial
      stale_key: should_be_removed
      nested:
        old_child: remove_me
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"sample": parseTestPluginConfig(t, `name: updated
nested:
  new_child: keep_me
`),
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	// In the reported bug, mergeMappingPreserve does not delete stale keys in mappings.
	if strings.Contains(savedText, "stale_key") {
		t.Errorf("saved config unexpectedly contains stale_key; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "old_child") {
		t.Errorf("saved config unexpectedly contains old_child; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "new_child: keep_me") {
		t.Errorf("saved config missing new_child: keep_me; got:\n%s", savedText)
	}
}

func TestSaveConfigPreserveComments_PluginConfigPreservesSequenceReordering(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `plugins:
  enabled: true
  configs:
    sample:
      vision_models:
        - name: model-a
          detail: detail-a
          old_prop: stale-a
        - name: model-b
          detail: detail-b
          old_prop: stale-b
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	// Reorder sequence: model-b first, then model-a, with detail modified.
	cfg := &Config{
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"sample": parseTestPluginConfig(t, `vision_models:
  - name: model-b
    detail: new-detail-b
    extra_b: only-in-b
  - name: model-a
    detail: new-detail-a
`),
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	idxB := strings.Index(savedText, "name: model-b")
	idxA := strings.Index(savedText, "name: model-a")
	if idxB == -1 || idxA == -1 {
		t.Fatalf("saved config missing model-a or model-b; got:\n%s", savedText)
	}
	if idxB > idxA {
		t.Errorf("expected model-b to precede model-a in reordered sequence; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "new-detail-b") || !strings.Contains(savedText, "new-detail-a") {
		t.Errorf("saved config missing updated details; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "extra_b: only-in-b") {
		t.Errorf("saved config missing extra_b; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "old_prop") || strings.Contains(savedText, "stale-") {
		t.Errorf("saved config unexpectedly contains stale properties from old sequence items; got:\n%s", savedText)
	}

	var loaded struct {
		Plugins struct {
			Configs map[string]struct {
				VisionModels []struct {
					Name   string `yaml:"name"`
					Detail string `yaml:"detail"`
					ExtraB string `yaml:"extra_b"`
				} `yaml:"vision_models"`
			} `yaml:"configs"`
		} `yaml:"plugins"`
	}
	if errUnmarshal := yaml.Unmarshal(savedBytes, &loaded); errUnmarshal != nil {
		t.Fatalf("yaml.Unmarshal(savedBytes) error = %v", errUnmarshal)
	}
	models := loaded.Plugins.Configs["sample"].VisionModels
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].Name != "model-b" || models[0].Detail != "new-detail-b" || models[0].ExtraB != "only-in-b" {
		t.Errorf("models[0] = %+v, want model-b with new-detail-b and only-in-b", models[0])
	}
	if models[1].Name != "model-a" || models[1].Detail != "new-detail-a" || models[1].ExtraB != "" {
		t.Errorf("models[1] = %+v, want model-a with new-detail-a and empty ExtraB", models[1])
	}
}

func TestSaveConfigPreserveComments_PluginConfigCreatesNewSubtree(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `port: 8080
debug: false
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	enabledFalse := false
	cfg := &Config{
		Port:  8080,
		Debug: false,
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"new-plugin": parseTestPluginConfig(t, `enabled: false
mode: minimal
count: 0
`),
			},
		},
	}
	cfg.Plugins.Configs["new-plugin"] = PluginInstanceConfig{
		Enabled: &enabledFalse,
		Raw:     cfg.Plugins.Configs["new-plugin"].Raw,
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	for _, expected := range []string{
		"new-plugin:",
		"enabled: false",
		"mode: minimal",
		"count: 0",
	} {
		if !strings.Contains(savedText, expected) {
			t.Errorf("saved config missing %q; got:\n%s", expected, savedText)
		}
	}
}

func TestSaveConfigPreserveComments_PluginConfigCreatesNewSubtreeWithOnlyZeroValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `port: 8080
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	enabledFalse := false
	cfg := &Config{
		Port: 8080,
		Plugins: PluginsConfig{
			Enabled: false,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"zero-plugin": parseTestPluginConfig(t, `enabled: false
timeout: 0
empty_str: ""
empty_list: []
`),
			},
		},
	}
	cfg.Plugins.Configs["zero-plugin"] = PluginInstanceConfig{
		Enabled: &enabledFalse,
		Raw:     cfg.Plugins.Configs["zero-plugin"].Raw,
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	for _, expected := range []string{
		"plugins:",
		"configs:",
		"zero-plugin:",
		"enabled: false",
		"timeout: 0",
		"empty_list: []",
	} {
		if !strings.Contains(savedText, expected) {
			t.Errorf("saved config missing %q; got:\n%s", expected, savedText)
		}
	}
}

func TestSaveConfigPreserveComments_PluginConfigDeletesPluginAndClearsConfigs(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `plugins:
  enabled: true
  configs:
    plugin-a:
      name: a
    plugin-b:
      name: b
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	// Step 1: remove plugin-a, keep plugin-b
	cfg := &Config{
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"plugin-b": parseTestPluginConfig(t, "name: b-updated\n"),
			},
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() step 1 error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() step 1 error = %v", errRead)
	}
	savedText := string(savedBytes)
	if strings.Contains(savedText, "plugin-a") {
		t.Errorf("saved config step 1 still contains plugin-a; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "plugin-b:") || !strings.Contains(savedText, "b-updated") {
		t.Errorf("saved config step 1 missing plugin-b; got:\n%s", savedText)
	}

	// Step 2: clear all configs
	cfg.Plugins.Configs = map[string]PluginInstanceConfig{}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() step 2 error = %v", errSave)
	}

	savedBytes, errRead = os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() step 2 error = %v", errRead)
	}
	savedText = string(savedBytes)
	if strings.Contains(savedText, "plugin-b") {
		t.Errorf("saved config step 2 still contains plugin-b; got:\n%s", savedText)
	}
	if strings.Contains(savedText, "configs:") {
		t.Errorf("saved config step 2 still contains configs section; got:\n%s", savedText)
	}
}

func TestSaveConfigPreserveComments_PluginConfigPreservesCommentsOutsidePlugins(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `# Server listening port
port: 8080
# Plugins configuration section
plugins:
  enabled: true
  configs:
    sample:
      name: old
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		Port: 8080,
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "plugins",
			Configs: map[string]PluginInstanceConfig{
				"sample": parseTestPluginConfig(t, "name: updated\nenabled: false\n"),
			},
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	if !strings.Contains(savedText, "# Server listening port") {
		t.Errorf("saved config missing port comment; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "# Plugins configuration section") {
		t.Errorf("saved config missing plugins comment; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "name: updated") || !strings.Contains(savedText, "enabled: false") {
		t.Errorf("saved config missing updated plugin config; got:\n%s", savedText)
	}
}

func TestSaveConfigPreserveComments_PluginConfigAddsConfigsWhenPluginsExistsWithoutConfigs(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initialYAML := `plugins:
  enabled: true
  dir: custom-dir
`
	if errWrite := os.WriteFile(configPath, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &Config{
		Plugins: PluginsConfig{
			Enabled: true,
			Dir:     "custom-dir",
			Configs: map[string]PluginInstanceConfig{
				"new-item": parseTestPluginConfig(t, "enabled: false\n"),
			},
		},
	}
	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)

	if !strings.Contains(savedText, "dir: custom-dir") {
		t.Errorf("saved config missing dir: custom-dir; got:\n%s", savedText)
	}
	if !strings.Contains(savedText, "new-item:") || !strings.Contains(savedText, "enabled: false") {
		t.Errorf("saved config missing new-item with enabled: false; got:\n%s", savedText)
	}
}
