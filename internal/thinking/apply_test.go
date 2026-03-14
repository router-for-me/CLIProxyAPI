package thinking

import "testing"

func TestExtractClaudeConfig_UsesOutputConfigEffort(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ThinkingConfig
	}{
		{
			name: "preserves max effort",
			body: `{"output_config":{"effort":"max"},"thinking":{"type":"adaptive"}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: "max"},
		},
		{
			name: "maps medium directly",
			body: `{"output_config":{"effort":"medium"}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelMedium},
		},
		{
			name: "none disables thinking",
			body: `{"output_config":{"effort":"none"},"thinking":{"type":"enabled","budget_tokens":2048}}`,
			want: ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name: "effort takes precedence over budget tokens",
			body: `{"output_config":{"effort":"low"},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelLow},
		},
		{
			name: "null effort falls back to budget tokens",
			body: `{"output_config":{"effort":null},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "empty effort falls back to budget tokens",
			body: `{"output_config":{"effort":""},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "whitespace effort falls back to budget tokens",
			body: `{"output_config":{"effort":"   "},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "numeric effort falls back to budget tokens",
			body: `{"output_config":{"effort":123},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "boolean effort falls back to budget tokens",
			body: `{"output_config":{"effort":false},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "unknown non-empty string still overrides budget tokens",
			body: `{"output_config":{"effort":"bogus"},"thinking":{"type":"enabled","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: "bogus"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractClaudeConfig([]byte(tt.body))
			if got.Mode != tt.want.Mode || got.Budget != tt.want.Budget || got.Level != tt.want.Level {
				t.Fatalf("extractClaudeConfig() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
