package config

import (
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPluginsConfigABIField(t *testing.T) {
	t.Run("explicit go", func(t *testing.T) {
		var cfg Config
		if err := yaml.Unmarshal([]byte("plugins:\n  enabled: true\n  abi: go\n"), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := cfg.Plugins.ABI; got != "go" {
			t.Fatalf("Plugins.ABI = %q, want %q", got, "go")
		}
	})

	t.Run("omitted defaults to empty", func(t *testing.T) {
		// The zero value must keep the existing C ABI loader as the default;
		// any non-empty value is an explicit opt-in to another mechanism.
		var cfg Config
		if err := yaml.Unmarshal([]byte("plugins:\n  enabled: true\n"), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := cfg.Plugins.ABI; got != "" {
			t.Fatalf("Plugins.ABI = %q, want empty (C ABI default)", got)
		}
	})

	t.Run("json tag round trip", func(t *testing.T) {
		cfg := Config{}
		cfg.Plugins.ABI = "go"
		raw, err := json.Marshal(cfg.Plugins)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded PluginsConfig
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.ABI != "go" {
			t.Fatalf("round-trip ABI = %q, want %q", decoded.ABI, "go")
		}
	})
}
