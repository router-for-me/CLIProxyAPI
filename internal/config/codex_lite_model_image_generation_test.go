package config

import "testing"

// TestParseConfigBytesCodexLiteModelImageGeneration ensures the canonical v8
// layout exposes the opt-in under multimedia and parses as a bool.
func TestParseConfigBytesCodexLiteModelImageGeneration(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
multimedia:
  codex-lite-model-image-generation: true
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.CodexLiteModelImageGeneration {
		t.Fatal("codex-lite-model-image-generation = false, want true")
	}
}

// TestParseConfigBytesCodexLiteModelImageGenerationLegacyLayout ensures the
// pre-v8 flat layout maps the key onto the same field.
func TestParseConfigBytesCodexLiteModelImageGenerationLegacyLayout(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
codex-lite-model-image-generation: true
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if !cfg.CodexLiteModelImageGeneration {
		t.Fatal("codex-lite-model-image-generation = false, want true")
	}
}

// TestParseConfigBytesCodexLiteModelImageGenerationDefaultOff ensures the
// opt-in defaults to false (the #7270 safeguard) when unset.
func TestParseConfigBytesCodexLiteModelImageGenerationDefaultOff(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
multimedia:
  gpt-image-2-base-model: "gpt-5.4-mini"
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if cfg.CodexLiteModelImageGeneration {
		t.Fatal("codex-lite-model-image-generation = true, want default false")
	}
}
