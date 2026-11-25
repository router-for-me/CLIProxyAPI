package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfigPreserveComments_PointerBoolExplicitFalse(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	falseVal := false
	cfg := &Config{
		Port: 8317,
		VertexCompatAPIKey: []VertexCompatKey{
			{
				APIKey:       "vertex-key-1",
				OpenAPI:      &falseVal,
				Interactions: &falseVal,
			},
		},
		CodexKey: []CodexKey{
			{
				APIKey:               "codex-key-1",
				BaseURL:              "https://example.invalid",
				DisableCodexCloaking: &falseVal,
			},
		},
	}

	if errSave := SaveConfigPreserveComments(configPath, cfg); errSave != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", errSave)
	}

	savedBytes, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedYAML := string(savedBytes)
	for _, want := range []string{"openapi: false", "interactions: false", "disable-codex-cloaking: false"} {
		if !strings.Contains(savedYAML, want) {
			t.Fatalf("saved YAML missing explicit %q:\n%s", want, savedYAML)
		}
	}

	loaded, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if len(loaded.VertexCompatAPIKey) != 1 {
		t.Fatalf("len(loaded.VertexCompatAPIKey) = %d, want 1", len(loaded.VertexCompatAPIKey))
	}
	if loaded.VertexCompatAPIKey[0].OpenAPI == nil || *loaded.VertexCompatAPIKey[0].OpenAPI {
		t.Fatalf("loaded.VertexCompatAPIKey[0].OpenAPI = %v, want explicit false", loaded.VertexCompatAPIKey[0].OpenAPI)
	}
	if loaded.VertexCompatAPIKey[0].Interactions == nil || *loaded.VertexCompatAPIKey[0].Interactions {
		t.Fatalf("loaded.VertexCompatAPIKey[0].Interactions = %v, want explicit false", loaded.VertexCompatAPIKey[0].Interactions)
	}
	if len(loaded.CodexKey) != 1 {
		t.Fatalf("len(loaded.CodexKey) = %d, want 1", len(loaded.CodexKey))
	}
	if loaded.CodexKey[0].DisableCodexCloaking == nil || *loaded.CodexKey[0].DisableCodexCloaking {
		t.Fatalf("loaded.CodexKey[0].DisableCodexCloaking = %v, want explicit false", loaded.CodexKey[0].DisableCodexCloaking)
	}
}
