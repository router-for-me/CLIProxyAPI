package thinking

import "testing"

func TestExtractEffortFromSuffix(t *testing.T) {
	if got := ExtractEffort(nil, "gpt-5.4(high)", "openai", "openai"); got != "high" {
		t.Fatalf("suffix high = %q, want high", got)
	}
	if got := ExtractEffort(nil, "gpt-5.4(4096)", "openai", "openai"); got != "budget:4096" {
		t.Fatalf("suffix budget = %q, want budget:4096", got)
	}
}

func TestExtractEffortPrefersBodyOverSuffix(t *testing.T) {
	body := []byte(`{"reasoning_effort":"low"}`)
	if got := ExtractEffort(body, "gpt-5.4(high)", "openai", "openai"); got != "low" {
		t.Fatalf("ExtractEffort() = %q, want low", got)
	}
}

func TestExtractEffortFromBodies(t *testing.T) {
	tests := []struct {
		name       string
		body       []byte
		fromFormat string
		toFormat   string
		want       string
	}{
		{name: "openai reasoning effort", body: []byte(`{"reasoning_effort":"medium"}`), fromFormat: "openai", toFormat: "openai", want: "medium"},
		{name: "codex reasoning effort", body: []byte(`{"reasoning":{"effort":"high"}}`), fromFormat: "codex", toFormat: "codex", want: "high"},
		{name: "openai response reasoning effort", body: []byte(`{"reasoning":{"effort":"high"}}`), fromFormat: "openai", toFormat: "openai-response", want: "high"},
		{name: "target format takes precedence", body: []byte(`{"reasoning_effort":"low","reasoning":{"effort":"high"}}`), fromFormat: "openai", toFormat: "openai-response", want: "high"},
		{name: "fallback to source format", body: []byte(`{"reasoning_effort":"medium"}`), fromFormat: "openai", toFormat: "openai-response", want: "medium"},
		{name: "claude budget", body: []byte(`{"thinking":{"type":"enabled","budget_tokens":4096}}`), fromFormat: "claude", toFormat: "claude", want: "budget:4096"},
		{name: "gemini level", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`), fromFormat: "gemini", toFormat: "gemini", want: "low"},
		{name: "gemini budget", body: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`), fromFormat: "gemini", toFormat: "gemini", want: "budget:1024"},
		{name: "level normalized", body: []byte(`{"reasoning_effort":"HIGH"}`), fromFormat: "openai", toFormat: "openai", want: "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractEffort(tt.body, "model", tt.fromFormat, tt.toFormat); got != tt.want {
				t.Fatalf("ExtractEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}
