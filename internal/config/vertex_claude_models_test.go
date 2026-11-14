package config

import "testing"

func TestV8VertexClaudeModelsPath(t *testing.T) {
	data := []byte("oauth:\n  providers:\n    vertex:\n      claude-models:\n        - \"claude-sonnet-5-5\"\n        - \"claude-haiku-5-5\"\n")
	if err := ValidateV8Config(data); err != nil {
		t.Fatalf("ValidateV8Config: %v", err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatalf("ParseConfigBytes: %v", err)
	}
	if len(cfg.VertexClaudeModels) != 2 || cfg.VertexClaudeModels[0] != "claude-sonnet-5-5" || cfg.VertexClaudeModels[1] != "claude-haiku-5-5" {
		t.Fatalf("VertexClaudeModels = %#v", cfg.VertexClaudeModels)
	}
}
