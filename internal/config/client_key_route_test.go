package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyRoutesV8RoundTrip(t *testing.T) {
	const source = `access:
  api-keys: [old-key, google-key]
  api-key-routes:
    - api-key: google-key
      provider: antigravity
      model: gemini-3.8-flash-high
      opus-model: gemini-4-argon-high
      sonnet-model: gemini-3.8-flash-high
      haiku-model: gemini-3-flash
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(source), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 2 || len(cfg.APIKeyRoutes) != 1 || cfg.APIKeyRoutes[0].APIKey != "google-key" || cfg.APIKeyRoutes[0].OpusModel != "gemini-4-argon-high" || cfg.APIKeyRoutes[0].SonnetModel != "gemini-3.8-flash-high" || cfg.APIKeyRoutes[0].HaikuModel != "gemini-3-flash" {
		t.Fatalf("access policy did not load: %+v", cfg.APIKeyRoutes)
	}
	clone := cfg.CloneForRuntime()
	clone.APIKeyRoutes[0].Model = "changed"
	if cfg.APIKeyRoutes[0].Model != "gemini-3.8-flash-high" {
		t.Fatal("clone aliases policy memory")
	}
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Config
	if err = yaml.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.APIKeyRoutes) != 1 || roundTrip.APIKeyRoutes[0] != cfg.APIKeyRoutes[0] {
		t.Fatal("policy lost on YAML round trip")
	}
}
