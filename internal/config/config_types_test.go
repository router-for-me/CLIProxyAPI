package config_test

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityAPIKeyNameRoundTrip(t *testing.T) {
	input := config.OpenAICompatibilityAPIKey{
		Name:     "team-a",
		APIKey:   "secret",
		ProxyURL: "direct",
	}

	rawYAML, err := yaml.Marshal(input)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	var gotYAML config.OpenAICompatibilityAPIKey
	if err := yaml.Unmarshal(rawYAML, &gotYAML); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if gotYAML.Name != input.Name {
		t.Fatalf("YAML Name = %q, want %q", gotYAML.Name, input.Name)
	}

	rawJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var gotJSON config.OpenAICompatibilityAPIKey
	if err := json.Unmarshal(rawJSON, &gotJSON); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if gotJSON.Name != input.Name {
		t.Fatalf("JSON Name = %q, want %q", gotJSON.Name, input.Name)
	}

	entry := store.UpstreamProviderAPIKey{Name: input.Name}
	if entry.Name != input.Name {
		t.Fatalf("store entry Name = %q, want %q", entry.Name, input.Name)
	}
}
