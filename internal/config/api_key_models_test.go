package config

import "testing"

func TestV8ClientKeyModelRestrictions(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("config-version: 8\naccess:\n  api-keys: [client, blocked]\n  api-key-models:\n    client: [public-model]\n    blocked: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeyModels["client"]) != 1 {
		t.Fatalf("missing restrictions: %#v", cfg.APIKeyModels)
	}
	if models, ok := cfg.APIKeyModels["blocked"]; !ok || len(models) != 0 {
		t.Fatal("empty deny-all policy lost")
	}
}
