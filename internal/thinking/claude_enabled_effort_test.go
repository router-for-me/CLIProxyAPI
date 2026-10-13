package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/openai"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ClaudeEnabledWithOutputConfigEffort(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{
			name:       "explicit output_config effort is preserved without budget",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`,
			wantEffort: "high",
		},
		{
			name:       "legacy budget remains authoritative when both are present",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"high"}}`,
			wantEffort: "medium",
		},
		{
			name:       "enabled without budget or effort keeps auto default",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"}}`,
			wantEffort: "auto",
		},
		{
			name:       "enabled with empty effort string falls back to auto",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"},"output_config":{"effort":""}}`,
			wantEffort: "auto",
		},
		{
			name:       "enabled with whitespace-only effort falls back to auto",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"},"output_config":{"effort":"   "}}`,
			wantEffort: "auto",
		},
		{
			name:       "enabled with non-string effort falls back to auto",
			body:       `{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"},"output_config":{"effort":123}}`,
			wantEffort: "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), "custom-openai", "claude", "openai", "openai")
			if err != nil {
				t.Fatalf("ApplyThinking returned error: %v", err)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tt.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
		})
	}
}

func TestClaudeToOpenAITranslationAndThinkingChained(t *testing.T) {
	rawClaude := []byte(`{"model":"custom-openai","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`)
	translated := openaiclaude.ConvertClaudeRequestToOpenAI("custom-openai", rawClaude, false)
	if got := gjson.GetBytes(translated, "reasoning_effort").String(); got != "high" {
		t.Fatalf("translated reasoning_effort = %q, want high; body=%s", got, translated)
	}

	final, err := thinking.ApplyThinkingWithSourceAndSummary(translated, rawClaude, "custom-openai", "claude", "openai", "openai", thinking.SummaryConfig{})
	if err != nil {
		t.Fatalf("ApplyThinkingWithSourceAndSummary returned error: %v", err)
	}
	if got := gjson.GetBytes(final, "reasoning_effort").String(); got != "high" {
		t.Fatalf("final reasoning_effort = %q, want high; body=%s", got, final)
	}
}
