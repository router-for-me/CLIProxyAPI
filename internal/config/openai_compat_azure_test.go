package config

import "testing"

func TestParseConfigBytesNormalizesOpenAICompatibilityAzure(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`openai-compatibility:
  - name: azure
    base-url: " https://resource.openai.azure.com/ "
    azure:
      deployment: " production/chat v1 "
      api-version: " 2025-04-01-preview "
    models:
      - name: upstream-model
        alias: public-model
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("provider count = %d, want 1", len(cfg.OpenAICompatibility))
	}
	got := cfg.OpenAICompatibility[0]
	if got.BaseURL != "https://resource.openai.azure.com/" {
		t.Fatalf("base-url = %q", got.BaseURL)
	}
	if got.Azure == nil {
		t.Fatal("azure config is nil")
	}
	if got.Azure.Deployment != "production/chat v1" {
		t.Fatalf("deployment = %q", got.Azure.Deployment)
	}
	if got.Azure.APIVersion != "2025-04-01-preview" {
		t.Fatalf("api-version = %q", got.Azure.APIVersion)
	}
	if got.Models[0].Name != "upstream-model" || got.Models[0].Alias != "public-model" {
		t.Fatalf("model mapping = %#v", got.Models[0])
	}
}
