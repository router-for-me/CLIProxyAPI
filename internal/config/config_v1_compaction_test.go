package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestV8GroupedConfigRoundTripsV1CompactionPerModel(t *testing.T) {
	const raw = `config-version: 8
server:
  port: 8317
api-keys:
  codex:
    - name: codex-provider
      base-url: https://codex.example.invalid/v1
      models:
        - name: shared-upstream
          alias: codex-v1
          use-v1-compaction: true
        - name: shared-upstream
          alias: codex-v2
      keys:
        - api-key: codex-test-key
  openai-compatibility:
    - name: compat-provider
      base-url: https://compat.example.invalid/v1
      models:
        - name: compat-upstream
          alias: compat-v1
          use-v1-compaction: true
        - name: compat-upstream
          alias: compat-v2
      keys:
        - api-key: compat-test-key
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.CodexKey) != 1 || len(cfg.CodexKey[0].Models) != 2 {
		t.Fatalf("loaded Codex models = %+v, want one grouped credential with two models", cfg.CodexKey)
	}
	if !cfg.CodexKey[0].Models[0].UseV1Compaction || cfg.CodexKey[0].Models[1].UseV1Compaction {
		t.Fatalf("loaded Codex v1-compaction settings = [%t %t], want [true false]", cfg.CodexKey[0].Models[0].UseV1Compaction, cfg.CodexKey[0].Models[1].UseV1Compaction)
	}
	if len(cfg.OpenAICompatibility) != 1 || len(cfg.OpenAICompatibility[0].Models) != 2 {
		t.Fatalf("loaded compatibility models = %+v, want one provider with two models", cfg.OpenAICompatibility)
	}
	if !cfg.OpenAICompatibility[0].Models[0].UseV1Compaction || cfg.OpenAICompatibility[0].Models[1].UseV1Compaction {
		t.Fatalf("loaded compatibility v1-compaction settings = [%t %t], want [true false]", cfg.OpenAICompatibility[0].Models[0].UseV1Compaction, cfg.OpenAICompatibility[0].Models[1].UseV1Compaction)
	}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if !reloaded.CodexKey[0].Models[0].UseV1Compaction || reloaded.CodexKey[0].Models[1].UseV1Compaction {
		t.Fatalf("saved Codex v1-compaction settings = [%t %t], want [true false]", reloaded.CodexKey[0].Models[0].UseV1Compaction, reloaded.CodexKey[0].Models[1].UseV1Compaction)
	}
	if !reloaded.OpenAICompatibility[0].Models[0].UseV1Compaction || reloaded.OpenAICompatibility[0].Models[1].UseV1Compaction {
		t.Fatalf("saved compatibility v1-compaction settings = [%t %t], want [true false]", reloaded.OpenAICompatibility[0].Models[0].UseV1Compaction, reloaded.OpenAICompatibility[0].Models[1].UseV1Compaction)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateV8Config(saved); err != nil {
		t.Fatalf("saved grouped config is invalid: %v", err)
	}
}
