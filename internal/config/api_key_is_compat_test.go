package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAPIKeyModelIsCompatConfigDecoding(t *testing.T) {
	const yamlConfig = `gemini-api-key:
  - models:
      - name: gemini-upstream
        alias: gemini-alias
        is-compat: true
      - name: gemini-native
        alias: gemini-native
interactions-api-key:
  - models:
      - name: interactions-upstream
        alias: interactions-alias
        is-compat: true
xai-api-key:
  - models:
      - name: xai-upstream
        alias: xai-alias
        is-compat: true
claude-api-key:
  - models:
      - name: claude-upstream
        alias: claude-alias
        is-compat: true
codex-api-key:
  - models:
      - name: codex-upstream
        alias: codex-alias
        is-compat: true
`

	var cfg Config
	if errDecode := yaml.Unmarshal([]byte(yamlConfig), &cfg); errDecode != nil {
		t.Fatalf("decode error: %v", errDecode)
	}

	if len(cfg.GeminiKey) != 1 || !cfg.GeminiKey[0].Models[0].IsCompat {
		t.Fatalf("gemini-api-key IsCompat = %+v, want true", cfg.GeminiKey)
	}
	if cfg.GeminiKey[0].Models[1].IsCompat {
		t.Fatal("gemini-api-key omitted IsCompat = true, want default false")
	}
	if len(cfg.InteractionsKey) != 1 || !cfg.InteractionsKey[0].Models[0].IsCompat {
		t.Fatalf("interactions-api-key IsCompat = %+v, want true", cfg.InteractionsKey)
	}
	if len(cfg.XAIKey) != 1 || !cfg.XAIKey[0].Models[0].IsCompat {
		t.Fatalf("xai-api-key IsCompat = %+v, want true", cfg.XAIKey)
	}
	if len(cfg.ClaudeKey) != 1 || !cfg.ClaudeKey[0].Models[0].IsCompat {
		t.Fatalf("claude-api-key IsCompat = %+v, want true", cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) != 1 || !cfg.CodexKey[0].Models[0].IsCompat {
		t.Fatalf("codex-api-key IsCompat = %+v, want true", cfg.CodexKey)
	}
}
