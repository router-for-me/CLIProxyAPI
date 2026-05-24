package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDisableImageGenerationMode_UnmarshalYAML(t *testing.T) {
	type wrapper struct {
		V DisableImageGenerationMode `yaml:"disable-image-generation"`
	}

	{
		var w wrapper
		if err := yaml.Unmarshal([]byte("disable-image-generation: false\n"), &w); err != nil {
			t.Fatalf("unmarshal false: %v", err)
		}
		if w.V != DisableImageGenerationOff {
			t.Fatalf("false => %v, want %v", w.V, DisableImageGenerationOff)
		}
	}

	{
		var w wrapper
		if err := yaml.Unmarshal([]byte("disable-image-generation: true\n"), &w); err != nil {
			t.Fatalf("unmarshal true: %v", err)
		}
		if w.V != DisableImageGenerationAll {
			t.Fatalf("true => %v, want %v", w.V, DisableImageGenerationAll)
		}
	}

	{
		var w wrapper
		if err := yaml.Unmarshal([]byte("disable-image-generation: chat\n"), &w); err != nil {
			t.Fatalf("unmarshal chat: %v", err)
		}
		if w.V != DisableImageGenerationChat {
			t.Fatalf("chat => %v, want %v", w.V, DisableImageGenerationChat)
		}
	}
}

func TestDisableImageGenerationMode_DefaultsToChat(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.DisableImageGeneration != DisableImageGenerationChat {
		t.Fatalf("ParseConfigBytes default = %v, want %v", cfg.DisableImageGeneration, DisableImageGenerationChat)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	loaded, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if loaded.DisableImageGeneration != DisableImageGenerationChat {
		t.Fatalf("LoadConfigOptional default = %v, want %v", loaded.DisableImageGeneration, DisableImageGenerationChat)
	}
}

func TestDisableImageGenerationMode_OptionalConfigDefaultsToChat(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.yaml")
	missing, err := LoadConfigOptional(missingPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional(missing, optional) error = %v", err)
	}
	if missing.DisableImageGeneration != DisableImageGenerationChat {
		t.Fatalf("missing optional default = %v, want %v", missing.DisableImageGeneration, DisableImageGenerationChat)
	}

	emptyPath := filepath.Join(t.TempDir(), "empty.yaml")
	if errWrite := os.WriteFile(emptyPath, nil, 0o600); errWrite != nil {
		t.Fatalf("write empty config: %v", errWrite)
	}
	empty, err := LoadConfigOptional(emptyPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional(empty, optional) error = %v", err)
	}
	if empty.DisableImageGeneration != DisableImageGenerationChat {
		t.Fatalf("empty optional default = %v, want %v", empty.DisableImageGeneration, DisableImageGenerationChat)
	}

	invalidPath := filepath.Join(t.TempDir(), "invalid.yaml")
	if errWrite := os.WriteFile(invalidPath, []byte("invalid: [\n"), 0o600); errWrite != nil {
		t.Fatalf("write invalid config: %v", errWrite)
	}
	invalid, err := LoadConfigOptional(invalidPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional(invalid, optional) error = %v", err)
	}
	if invalid.DisableImageGeneration != DisableImageGenerationChat {
		t.Fatalf("invalid optional default = %v, want %v", invalid.DisableImageGeneration, DisableImageGenerationChat)
	}
}

func TestDisableImageGenerationMode_IsKnownDefaultValue(t *testing.T) {
	chatNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "chat"}
	if !isKnownDefaultValue([]string{"disable-image-generation"}, chatNode) {
		t.Fatalf("disable-image-generation=chat should be treated as a known default")
	}

	falseNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
	if isKnownDefaultValue([]string{"disable-image-generation"}, falseNode) {
		t.Fatalf("disable-image-generation=false should be preserved as an explicit non-default")
	}
}

func TestDisableImageGenerationMode_UnmarshalJSON(t *testing.T) {
	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte("false"), &v); err != nil {
			t.Fatalf("unmarshal false: %v", err)
		}
		if v != DisableImageGenerationOff {
			t.Fatalf("false => %v, want %v", v, DisableImageGenerationOff)
		}
	}

	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte("true"), &v); err != nil {
			t.Fatalf("unmarshal true: %v", err)
		}
		if v != DisableImageGenerationAll {
			t.Fatalf("true => %v, want %v", v, DisableImageGenerationAll)
		}
	}

	{
		var v DisableImageGenerationMode
		if err := json.Unmarshal([]byte(`"chat"`), &v); err != nil {
			t.Fatalf("unmarshal chat: %v", err)
		}
		if v != DisableImageGenerationChat {
			t.Fatalf("chat => %v, want %v", v, DisableImageGenerationChat)
		}
	}
}
