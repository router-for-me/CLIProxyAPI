package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigYAMLAddsHostDefaultsToRawPluginConfig(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("config1: true\nconfig2: value\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if len(node.Content) != 1 {
		t.Fatalf("yaml node content length = %d, want 1", len(node.Content))
	}
	item := config.PluginInstanceConfig{
		Priority: 3,
		Raw:      *node.Content[0],
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"config1: true",
		"config2: value",
		"enabled: true",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}
