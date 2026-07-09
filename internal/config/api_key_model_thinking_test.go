package config

import "testing"

func TestParseClaudeModelThinking(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
claude-api-key:
  - api-key: test-key
    models:
      - name: claude-opus-4-6
        thinking:
          levels:
            - high
            - medium
            - low
            - minimal
            - none
            - auto
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.ClaudeKey) != 1 || len(cfg.ClaudeKey[0].Models) != 1 {
		t.Fatalf("parsed claude models = %#v", cfg.ClaudeKey)
	}
	thinking := cfg.ClaudeKey[0].Models[0].Thinking
	if thinking == nil {
		t.Fatal("Thinking = nil, want configured support")
	}
	if got, want := len(thinking.Levels), 6; got != want {
		t.Fatalf("Thinking.Levels count = %d, want %d", got, want)
	}
}
