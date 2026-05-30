package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAICompatibilityUnmarshalAzureSnakeCaseAliases(t *testing.T) {
	var cfg Config
	input := []byte(`openai-compatibility:
  - name: azure
    base-url: https://example.openai.azure.com
    api_type: azure-openai
    api_version: 2025-04-01-preview
    deployment_id: prod-gpt-4-1
    path_mode: deployment
    auth_type: aad
    include_usage: false
    api-key-entries:
      - api-key: token
    models:
      - name: prod-gpt-4-1
        alias: azure-gpt-4.1
`)

	if err := yaml.Unmarshal(input, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("OpenAICompatibility length = %d", len(cfg.OpenAICompatibility))
	}
	compat := cfg.OpenAICompatibility[0]
	if compat.APIType != "azure-openai" {
		t.Fatalf("APIType = %q", compat.APIType)
	}
	if compat.APIVersion != "2025-04-01-preview" {
		t.Fatalf("APIVersion = %q", compat.APIVersion)
	}
	if compat.Deployment != "prod-gpt-4-1" {
		t.Fatalf("Deployment = %q", compat.Deployment)
	}
	if compat.PathMode != "deployment" {
		t.Fatalf("PathMode = %q", compat.PathMode)
	}
	if compat.AuthType != "aad" {
		t.Fatalf("AuthType = %q", compat.AuthType)
	}
	if compat.IncludeUsage == nil || *compat.IncludeUsage {
		t.Fatalf("IncludeUsage = %v", compat.IncludeUsage)
	}
}
