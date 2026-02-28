package thinking

import "testing"

func TestExtractClaudeConfig_UsesOutputConfigEffort(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ThinkingConfig
	}{
		{
			name: "maps max to xhigh",
			body: `{"output_config":{"effort":"max"},"thinking":{"type":"adaptive"}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh},
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
