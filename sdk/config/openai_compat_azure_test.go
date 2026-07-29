package config

import "testing"

func TestOpenAICompatibilityAzureSDKExposure(t *testing.T) {
	cfg := Config{OpenAICompatibility: []OpenAICompatibility{{
		Name:    "azure",
		BaseURL: "https://resource.openai.azure.com",
		Azure: &OpenAICompatibilityAzure{
			Deployment: "deployment-one",
			APIVersion: "2025-04-01-preview",
		},
	}}}
	if cfg.OpenAICompatibility[0].Azure.Deployment != "deployment-one" {
		t.Fatalf("deployment = %q", cfg.OpenAICompatibility[0].Azure.Deployment)
	}
}
