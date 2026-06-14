package thinking

import (
	"testing"
)

func TestStripThinkingConfig(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		provider string
		want     string
	}{
		// Claude
		{
			name:     "claude strips thinking and output_config.effort",
			provider: "claude",
			body:     []byte(`{"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"high"}}`),
			want:     `{}`,
		},
		{
			name:     "claude removes empty output_config",
			provider: "claude",
			body:     []byte(`{"thinking":{"type":"enabled"},"output_config":{}}`),
			want:     `{}`,
		},
		// Gemini
		{
			name:     "gemini strips generationConfig.thinkingConfig",
			provider: "gemini",
			body:     []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}`),
			want:     `{"generationConfig":{}}`,
		},
		// Gemini CLI / Antigravity
		{
			name:     "gemini-cli strips request.generationConfig.thinkingConfig",
			provider: "gemini-cli",
			body:     []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"medium"}}}}`),
			want:     `{"request":{"generationConfig":{}}}`,
		},
		{
			name:     "antigravity strips same as gemini-cli",
			provider: "antigravity",
			body:     []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingBudget":8192}}}}`),
			want:     `{"request":{"generationConfig":{}}}`,
		},
		// OpenAI
		{
			name:     "openai strips reasoning_effort",
			provider: "openai",
			body:     []byte(`{"reasoning_effort":"high"}`),
			want:     `{}`,
		},
		// Codex / xAI
		{
			name:     "codex strips reasoning.effort",
			provider: "codex",
			body:     []byte(`{"reasoning":{"effort":"medium"}}`),
			want:     `{"reasoning":{}}`,
		},
		{
			name:     "xai strips reasoning.effort",
			provider: "xai",
			body:     []byte(`{"reasoning":{"effort":"high"}}`),
			want:     `{"reasoning":{}}`,
		},
		// Kimi / DeepSeek
		{
			name:     "kimi strips reasoning_effort and thinking",
			provider: "kimi",
			body:     []byte(`{"reasoning_effort":"high","thinking":{"type":"enabled"}}`),
			want:     `{}`,
		},
		{
			name:     "deepseek strips reasoning_effort and thinking",
			provider: "deepseek",
			body:     []byte(`{"reasoning_effort":"medium","thinking":{"type":"enabled"}}`),
			want:     `{}`,
		},
		// Edge cases
		{
			name:     "empty body",
			provider: "claude",
			body:     []byte{},
			want:     "",
		},
		{
			name:     "invalid JSON body",
			provider: "claude",
			body:     []byte(`{invalid}`),
			want:     `{invalid}`,
		},
		{
			name:     "unknown provider returns body as-is",
			provider: "unknown",
			body:     []byte(`{"thinking":{"type":"enabled"}}`),
			want:     `{"thinking":{"type":"enabled"}}`,
		},
		{
			name:     "no thinking config to strip",
			provider: "openai",
			body:     []byte(`{"model":"gpt-4"}`),
			want:     `{"model":"gpt-4"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripThinkingConfig(tt.body, tt.provider)
			if string(got) != tt.want {
				t.Errorf("StripThinkingConfig() = %s, want %s", got, tt.want)
			}
		})
	}
}
