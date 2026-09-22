package config

import "testing"

func TestParseConfigBytesRequestRetry(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
gemini-api-key:
  - api-key: "gemini-zero"
    request-retry: 0
  - api-key: "gemini-unset"
interactions-api-key:
  - api-key: "interactions-two"
    request-retry: 2
codex-api-key:
  - api-key: "codex-neg"
    base-url: "https://codex.example.com"
    request-retry: -1
xai-api-key:
  - api-key: "xai-zero"
    base-url: "https://api.x.ai/v1"
    request-retry: 0
claude-api-key:
  - api-key: "claude-three"
    request-retry: 3
openai-compatibility:
  - name: "compat"
    base-url: "https://compat.example.com/v1"
    request-retry: 0
    api-key-entries:
      - api-key: "compat-key"
vertex-api-key:
  - api-key: "vertex-four"
    request-retry: 4
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("gemini-api-key count = %d, want 2", len(cfg.GeminiKey))
	}
	if cfg.GeminiKey[0].RequestRetry == nil || *cfg.GeminiKey[0].RequestRetry != 0 {
		t.Fatalf("gemini[0].request-retry = %v, want 0", cfg.GeminiKey[0].RequestRetry)
	}
	if cfg.GeminiKey[1].RequestRetry != nil {
		t.Fatalf("gemini[1].request-retry = %v, want unset", cfg.GeminiKey[1].RequestRetry)
	}
	if len(cfg.InteractionsKey) != 1 || cfg.InteractionsKey[0].RequestRetry == nil || *cfg.InteractionsKey[0].RequestRetry != 2 {
		t.Fatalf("interactions[0].request-retry = %v, want 2", valueOrNil(cfg.InteractionsKey))
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].RequestRetry == nil || *cfg.CodexKey[0].RequestRetry != -1 {
		t.Fatalf("codex[0].request-retry = %v, want -1", valueOrNil(cfg.CodexKey))
	}
	if len(cfg.XAIKey) != 1 || cfg.XAIKey[0].RequestRetry == nil || *cfg.XAIKey[0].RequestRetry != 0 {
		t.Fatalf("xai[0].request-retry = %v, want 0", valueOrNil(cfg.XAIKey))
	}
	if len(cfg.ClaudeKey) != 1 || cfg.ClaudeKey[0].RequestRetry == nil || *cfg.ClaudeKey[0].RequestRetry != 3 {
		t.Fatalf("claude[0].request-retry = %v, want 3", valueOrNil(cfg.ClaudeKey))
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].RequestRetry == nil || *cfg.OpenAICompatibility[0].RequestRetry != 0 {
		t.Fatalf("openai-compatibility[0].request-retry = %v, want 0", cfg.OpenAICompatibility[0].RequestRetry)
	}
	if len(cfg.VertexCompatAPIKey) != 1 || cfg.VertexCompatAPIKey[0].RequestRetry == nil || *cfg.VertexCompatAPIKey[0].RequestRetry != 4 {
		t.Fatalf("vertex[0].request-retry = %v, want 4", cfg.VertexCompatAPIKey[0].RequestRetry)
	}
}

func valueOrNil[T any](items []T) any {
	if len(items) == 0 {
		return nil
	}
	return items[0]
}
