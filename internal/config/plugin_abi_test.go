package config

import (
	"reflect"
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

	t.Run("abi field carries the expected wire tags", func(t *testing.T) {
		// The struct tags are asserted directly instead of round-tripping
		// through a JSON library: the repository requires JSON handling to go
		// through MoeForge/moego, and a tag check needs no JSON at all.
		field, ok := reflect.TypeOf(PluginsConfig{}).FieldByName("ABI")
		if !ok {
			t.Fatal("PluginsConfig has no ABI field")
		}
		if got := field.Tag.Get("yaml"); got != "abi,omitempty" {
			t.Fatalf("ABI yaml tag = %q, want %q", got, "abi,omitempty")
		}
		if got := field.Tag.Get("json"); got != "abi,omitempty" {
			t.Fatalf("ABI json tag = %q, want %q", got, "abi,omitempty")
		}
	})
}
