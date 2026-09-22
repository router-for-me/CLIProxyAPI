package config

import (
	"testing"
)

func TestParseConfigRequestScopedErrors(t *testing.T) {
	const yamlConfig = `
gemini-api-key:
  - api-key: gemini-key-1
    request-scoped-errors:
      - status: 400
        match:
          - "maximum_context_length"
          - "context_length_exceeded"
        match-regexr:
          - "maximum_context_length$"
          - "^context_length_exceeded"
        action: stop

interactions-api-key:
  - api-key: interactions-key-1
    request-scoped-errors:
      - status: 400
        match:
          - "invalid_argument"
        action: continue

codex-api-key:
  - api-key: codex-key-1
    base-url: https://api.openai.com/v1
    request-scoped-errors:
      - status: 400
        match:
          - "context_window_exceeded"
        action: stop-and-cooldown

xai-api-key:
  - api-key: xai-key-1
    base-url: https://api.x.ai/v1
    request-scoped-errors:
      - status: 500
        match:
          - "rate_limit_exceeded"
        action: continue-and-cooldown

claude-api-key:
  - api-key: claude-key-1
    request-scoped-errors:
      - status: 400
        match:
          - "prompt is too long"
        action: stop

openai-compatibility:
  - name: test-openai-compat
    base-url: https://api.openai.compat/v1
    api-key-entries:
      - api-key: compat-key-1
    request-scoped-errors:
      - status: 400
        match:
          - maximum_context_length
          - context_length_exceeded
        match-regexr:
          - "maximum_context_length$"
          - "^context_length_exceeded"
        action: stop
`

	cfg, errParse := ParseConfigBytes([]byte(yamlConfig))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	if len(cfg.GeminiKey) != 1 || len(cfg.GeminiKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("gemini[0].request-scoped-errors len = %d, want 1", len(cfg.GeminiKey[0].RequestScopedErrors))
	}
	gRule := cfg.GeminiKey[0].RequestScopedErrors[0]
	if gRule.Status != 400 || len(gRule.Match) != 2 || len(gRule.MatchRegexr) != 2 || gRule.Action != "stop" {
		t.Fatalf("unexpected gemini rule: %+v", gRule)
	}

	if len(cfg.InteractionsKey) != 1 || len(cfg.InteractionsKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("interactions[0].request-scoped-errors len = %d, want 1", len(cfg.InteractionsKey[0].RequestScopedErrors))
	}
	iRule := cfg.InteractionsKey[0].RequestScopedErrors[0]
	if iRule.Status != 400 || len(iRule.Match) != 1 || iRule.Action != "continue" {
		t.Fatalf("unexpected interactions rule: %+v", iRule)
	}

	if len(cfg.CodexKey) != 1 || len(cfg.CodexKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("codex[0].request-scoped-errors len = %d, want 1", len(cfg.CodexKey[0].RequestScopedErrors))
	}
	codexRule := cfg.CodexKey[0].RequestScopedErrors[0]
	if codexRule.Status != 400 || codexRule.Action != "stop-and-cooldown" {
		t.Fatalf("unexpected codex rule: %+v", codexRule)
	}

	if len(cfg.XAIKey) != 1 || len(cfg.XAIKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("xai[0].request-scoped-errors len = %d, want 1", len(cfg.XAIKey[0].RequestScopedErrors))
	}
	xaiRule := cfg.XAIKey[0].RequestScopedErrors[0]
	if xaiRule.Status != 500 || xaiRule.Action != "continue-and-cooldown" {
		t.Fatalf("unexpected xai rule: %+v", xaiRule)
	}

	if len(cfg.ClaudeKey) != 1 || len(cfg.ClaudeKey[0].RequestScopedErrors) != 1 {
		t.Fatalf("claude[0].request-scoped-errors len = %d, want 1", len(cfg.ClaudeKey[0].RequestScopedErrors))
	}
	claudeRule := cfg.ClaudeKey[0].RequestScopedErrors[0]
	if claudeRule.Status != 400 || claudeRule.Action != "stop" {
		t.Fatalf("unexpected claude rule: %+v", claudeRule)
	}

	if len(cfg.OpenAICompatibility) != 1 || len(cfg.OpenAICompatibility[0].RequestScopedErrors) != 1 {
		t.Fatalf("openai-compatibility[0].request-scoped-errors len = %d, want 1", len(cfg.OpenAICompatibility[0].RequestScopedErrors))
	}
	compatRule := cfg.OpenAICompatibility[0].RequestScopedErrors[0]
	if compatRule.Status != 400 || len(compatRule.Match) != 2 || len(compatRule.MatchRegexr) != 2 || compatRule.Action != "stop" {
		t.Fatalf("unexpected openai-compatibility rule: %+v", compatRule)
	}
}
