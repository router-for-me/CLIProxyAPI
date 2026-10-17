package thinking

import (
	"testing"
)

func TestExtractOpenAIConfig_KelivoCompatibility(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantMode   ThinkingMode
		wantBudget int
		wantLevel  ThinkingLevel
	}{
		{
			name:       "Kelivo OpenRouter custom budget",
			body:       `{"model":"gemini-3.8-flash","reasoning":{"enabled":true,"max_tokens":2048},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeBudget,
			wantBudget: 2048,
		},
		{
			name:      "Kelivo OpenRouter effort",
			body:      `{"model":"gemini-3.8-flash","reasoning":{"effort":"low"},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:  ModeLevel,
			wantLevel: LevelLow,
		},
		{
			name:       "Kelivo OpenRouter disabled",
			body:       `{"model":"gemini-3.8-flash","reasoning":{"enabled":false},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeNone,
			wantBudget: 0,
		},
		{
			name:       "Kelivo Anthropic custom budget",
			body:       `{"model":"gemini-3.8-flash","thinking":{"type":"enabled","budget_tokens":4096},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeBudget,
			wantBudget: 4096,
		},
		{
			name:       "Kelivo Anthropic disabled",
			body:       `{"model":"gemini-3.8-flash","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeNone,
			wantBudget: 0,
		},
		{
			name:       "Kelivo Qwen custom budget",
			body:       `{"model":"gemini-3.8-flash","thinking_budget":1024,"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeBudget,
			wantBudget: 1024,
		},
		{
			name:       "Kelivo Qwen disabled",
			body:       `{"model":"gemini-3.8-flash","enable_thinking":false,"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeNone,
			wantBudget: 0,
		},
		{
			name:       "Kelivo Gemini custom budget",
			body:       `{"model":"gemini-3.8-flash","generationConfig":{"thinkingConfig":{"thinkingBudget":8192,"includeThoughts":true}},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:   ModeBudget,
			wantBudget: 8192,
		},
		{
			name:      "Kelivo Gemini level",
			body:      `{"model":"gemini-3.8-flash","generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH","includeThoughts":true}},"messages":[{"role":"user","content":"hi"}]}`,
			wantMode:  ModeLevel,
			wantLevel: LevelHigh,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := extractOpenAIConfig([]byte(tc.body))
			if cfg.Mode != tc.wantMode {
				t.Fatalf("Mode = %v, want %v", cfg.Mode, tc.wantMode)
			}
			if tc.wantMode == ModeBudget && cfg.Budget != tc.wantBudget {
				t.Fatalf("Budget = %d, want %d", cfg.Budget, tc.wantBudget)
			}
			if tc.wantMode == ModeLevel && cfg.Level != tc.wantLevel {
				t.Fatalf("Level = %s, want %s", cfg.Level, tc.wantLevel)
			}
		})
	}
}
