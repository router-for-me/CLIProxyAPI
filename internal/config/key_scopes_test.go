package config

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientKeyScopesConfigRoundTrip(t *testing.T) {
	raw := []byte(`config-version: 8
client:
  key-scopes:
    - key-prefix: sk-claude-
      providers: [claude]
    - key-prefix: sk-codex-
      providers: [codex, openai]
`)
	if err := ValidateV8Config(raw); err != nil {
		t.Fatalf("v8 validation rejected key-scopes: %v", err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Client.KeyScopes == nil || len(*cfg.Client.KeyScopes) != 2 || (*cfg.Client.KeyScopes)[1].Providers[1] != "openai" {
		t.Fatalf("key-scopes = %+v", cfg.Client.KeyScopes)
	}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseConfigBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Client, reloaded.Client) {
		t.Fatal("key-scopes lost on round trip")
	}
	clone := cfg.CloneForRuntime()
	(*cfg.Client.KeyScopes)[0].Providers[0] = "codex"
	if (*clone.Client.KeyScopes)[0].Providers[0] != "claude" {
		t.Fatal("runtime snapshot shares key-scopes")
	}
}
