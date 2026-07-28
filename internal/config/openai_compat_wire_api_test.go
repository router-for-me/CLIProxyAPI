package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityWireAPINormalizationAndRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "responses", raw: "  ReSpOnSeS  ", want: "responses"},
		{name: "unknown preserved", raw: "  FuTuRe-PrOtOcOl  ", want: "future-protocol"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte("openai-compatibility:\n  - name: test\n    base-url: https://example.com/v1\n    wire-api: \"" + test.raw + "\"\n"))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes() error = %v", errParse)
			}
			if got := cfg.OpenAICompatibility[0].WireAPI; got != test.want {
				t.Fatalf("WireAPI = %q, want %q", got, test.want)
			}

			yamlData, errYAML := yaml.Marshal(cfg.OpenAICompatibility[0])
			if errYAML != nil {
				t.Fatalf("yaml.Marshal() error = %v", errYAML)
			}
			if !strings.Contains(string(yamlData), "wire-api: "+test.want) {
				t.Fatalf("YAML does not contain normalized wire-api: %s", yamlData)
			}

			jsonData, errJSON := json.Marshal(cfg.OpenAICompatibility[0])
			if errJSON != nil {
				t.Fatalf("json.Marshal() error = %v", errJSON)
			}
			var decoded OpenAICompatibility
			if errUnmarshal := json.Unmarshal(jsonData, &decoded); errUnmarshal != nil {
				t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
			}
			if decoded.WireAPI != test.want {
				t.Fatalf("JSON round-trip WireAPI = %q, want %q", decoded.WireAPI, test.want)
			}
		})
	}
}

func TestOpenAICompatibilityWireAPIOmittedWhenEmpty(t *testing.T) {
	entry := OpenAICompatibility{Name: "test", BaseURL: "https://example.com/v1"}

	yamlData, errYAML := yaml.Marshal(entry)
	if errYAML != nil {
		t.Fatalf("yaml.Marshal() error = %v", errYAML)
	}
	if strings.Contains(string(yamlData), "wire-api") {
		t.Fatalf("empty wire-api should be omitted from YAML: %s", yamlData)
	}

	jsonData, errJSON := json.Marshal(entry)
	if errJSON != nil {
		t.Fatalf("json.Marshal() error = %v", errJSON)
	}
	if strings.Contains(string(jsonData), "wire-api") {
		t.Fatalf("empty wire-api should be omitted from JSON: %s", jsonData)
	}
}
