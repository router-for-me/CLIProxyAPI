package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func TestExtractReasoningEffort_ExtraBodyGoogleThinkingConfig(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  string
		want  string
	}{
		{
			name:  "snake_case thinking_level",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","messages":[{"role":"user","content":"hi"}],"extra_body":{"google":{"thinking_config":{"include_thoughts":true,"thinking_level":"xhigh"}}}}`,
			want:  "xhigh",
		},
		{
			name:  "camelCase thinkingConfig thinkingLevel",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinkingConfig":{"thinkingLevel":" HIGH "}}}}`,
			want:  "high",
		},
		{
			name:  "snake_case thinking_budget maps to level",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"thinking_budget":8192}}}}`,
			want:  "medium",
		},
		{
			name:  "camelCase thinkingBudget zero maps to none",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinkingConfig":{"thinkingBudget":0}}}}`,
			want:  "none",
		},
		{
			name:  "nested extra_body.extra_body.google",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"extra_body":{"google":{"thinking_config":{"thinking_level":"low"}}}}}`,
			want:  "low",
		},
		{
			name:  "top-level google.thinking_config",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","google":{"thinking_config":{"thinking_level":"medium"}}}`,
			want:  "medium",
		},
		{
			name:  "top-level reasoning_effort takes precedence over extra_body",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","reasoning_effort":"low","extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  "low",
		},
		{
			name:  "generationConfig takes precedence over extra_body",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}},"extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  "low",
		},
		{
			name:  "model suffix takes precedence over extra_body",
			model: "gemini-4-neon-xhigh(low)",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  "low",
		},
		{
			name:  "invalid field types are ignored",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"thinking_level":123,"thinking_budget":"8192"}}}}`,
			want:  "",
		},
		{
			name:  "empty thinking_level falls back to thinking_budget",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"thinking_level":"  ","thinking_budget":8192}}}}`,
			want:  "medium",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := thinking.ExtractReasoningEffort([]byte(tc.body), "openai", tc.model); got != tc.want {
				t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequestedAntigravityThinkingLevel_ExtraBodyGoogleThinkingConfig(t *testing.T) {
	tests := []struct {
		name  string
		model string
		body  string
		want  thinking.ThinkingLevel
	}{
		{
			name:  "snake_case thinking_level",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"include_thoughts":true,"thinking_level":"xhigh"}}}}`,
			want:  thinking.LevelXHigh,
		},
		{
			name:  "camelCase thinkingConfig thinkingLevel",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}}`,
			want:  thinking.LevelHigh,
		},
		{
			name:  "snake_case thinking_budget",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","extra_body":{"google":{"thinking_config":{"thinking_budget":8192}}}}`,
			want:  thinking.LevelMedium,
		},
		{
			name:  "top-level reasoning_effort overrides extra_body",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","reasoning_effort":"low","extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  thinking.LevelLow,
		},
		{
			name:  "generationConfig overrides extra_body",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}},"extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  thinking.LevelLow,
		},
		{
			name:  "generation_config overrides extra_body",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","generation_config":{"thinking_config":{"thinking_level":"low"}},"extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  thinking.LevelLow,
		},
		{
			name:  "generationConfig with only includeThoughts still falls back to extra_body thinking_level",
			model: "gemini-4-neon-xhigh",
			body:  `{"model":"gemini-4-neon-xhigh","generationConfig":{"thinkingConfig":{"includeThoughts":true}},"extra_body":{"google":{"thinking_config":{"thinking_level":"xhigh"}}}}`,
			want:  thinking.LevelXHigh,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := thinking.RequestedAntigravityThinkingLevel([]byte(tc.body), "openai", tc.model); got != tc.want {
				t.Fatalf("RequestedAntigravityThinkingLevel() = %q, want %q", got, tc.want)
			}
		})
	}
}
