package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestV8MetaLiveModels(t *testing.T) {
	raw := []byte(`server: {port: 8317}
oauth:
  providers:
    meta:
      live-models: true
`)
	if err := ValidateV8Config(raw); err != nil {
		t.Fatalf("oauth.providers.meta.live-models is not a valid setting: %v", err)
	}
	cfg, err := ParseConfigBytes(raw)
	if err != nil || !cfg.Meta.LiveModels {
		t.Fatalf("oauth.providers.meta.live-models did not turn on Meta live models: error=%v", err)
	}

	migrated, _, err := NormalizeConfigLayout([]byte("port: 8317\nmeta:\n  live-models: true\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated config is invalid: %v\n%s", err, migrated)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(migrated, &doc); err != nil {
		t.Fatal(err)
	}
	if value := yamlPath(doc.Content[0], "oauth.providers.meta.live-models"); value == nil || value.Value != "true" {
		t.Fatalf("legacy meta.live-models did not move to oauth.providers.meta.live-models:\n%s", migrated)
	}
}
