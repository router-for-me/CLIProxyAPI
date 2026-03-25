package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeTopLevelAPIKeysYAML(t *testing.T) {
	input := `api-keys:
  - name: primary
    api-key: sk-primary
  - sk-secondary
debug: true
`

	normalized, entries, err := NormalizeTopLevelAPIKeysYAML([]byte(input))
	if err != nil {
		t.Fatalf("unexpected normalize error: %v", err)
	}
	if got, want := len(entries), 2; got != want {
		t.Fatalf("entries length = %d, want %d", got, want)
	}
	if entries[0].Name != "primary" || entries[0].APIKey != "sk-primary" {
		t.Fatalf("first entry = %#v", entries[0])
	}
	if entries[1].Name != "" || entries[1].APIKey != "sk-secondary" {
		t.Fatalf("second entry = %#v", entries[1])
	}

	var cfg Config
	if err := yaml.Unmarshal(normalized, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if got, want := len(cfg.APIKeys), 2; got != want {
		t.Fatalf("APIKeys length = %d, want %d", got, want)
	}
	if cfg.APIKeys[0] != "sk-primary" || cfg.APIKeys[1] != "sk-secondary" {
		t.Fatalf("APIKeys = %#v", cfg.APIKeys)
	}
	if !strings.Contains(string(normalized), "- sk-primary") || !strings.Contains(string(normalized), "- sk-secondary") {
		t.Fatalf("normalized yaml did not contain plain string api-keys:\n%s", string(normalized))
	}
}

func TestBuildTopLevelAPIKeysJSONValue(t *testing.T) {
	value := BuildTopLevelAPIKeysJSONValue([]NamedTopLevelAPIKey{
		{Name: "primary", APIKey: "sk-primary"},
		{APIKey: "sk-secondary"},
	})
	if got, want := len(value), 2; got != want {
		t.Fatalf("value length = %d, want %d", got, want)
	}
	first, ok := value[0].(map[string]string)
	if !ok {
		t.Fatalf("first value type = %T", value[0])
	}
	if first["name"] != "primary" || first["api-key"] != "sk-primary" {
		t.Fatalf("first value = %#v", first)
	}
	second, ok := value[1].(string)
	if !ok || second != "sk-secondary" {
		t.Fatalf("second value = %#v", value[1])
	}
}
