package config

import "testing"

func TestParseConfig_OpenCode(t *testing.T) {
	data := []byte(`
opencode:
  force-model-mappings: true
  model-mappings:
    - from: "claude-sonnet-4-5"
      to: "gpt-5"
    - from: "^gpt-5.*$"
      to: "claude-sonnet-4-5-20250929"
      regex: true
`)

	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatalf("ParseConfigBytes returned error: %v", err)
	}

	if !cfg.OpenCode.ForceModelMappings {
		t.Errorf("expected force-model-mappings true")
	}
	if got := len(cfg.OpenCode.ModelMappings); got != 2 {
		t.Fatalf("expected 2 model mappings, got %d", got)
	}
	first := cfg.OpenCode.ModelMappings[0]
	if first.From != "claude-sonnet-4-5" || first.To != "gpt-5" || first.Regex {
		t.Errorf("unexpected first mapping: %+v", first)
	}
	second := cfg.OpenCode.ModelMappings[1]
	if second.From != "^gpt-5.*$" || second.To != "claude-sonnet-4-5-20250929" || !second.Regex {
		t.Errorf("unexpected second mapping: %+v", second)
	}
}

func TestParseConfig_OpenCode_DefaultsEmpty(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes returned error: %v", err)
	}
	if cfg.OpenCode.ForceModelMappings {
		t.Errorf("expected force-model-mappings to default to false")
	}
	if len(cfg.OpenCode.ModelMappings) != 0 {
		t.Errorf("expected no model mappings by default, got %d", len(cfg.OpenCode.ModelMappings))
	}
}
