package thinking

import "testing"

func TestExtractCodexConfig_PrefersReasoningEffortOverVariant(t *testing.T) {
	body := []byte(`{"reasoning":{"effort":"high"},"variant":"low"}`)
	cfg := extractCodexConfig(body)

	if cfg.Mode != ModeLevel || cfg.Level != LevelHigh {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestExtractCodexConfig_VariantFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ThinkingConfig
	}{
		{
			name: "high",
			body: `{"variant":"high"}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name: "x-high alias",
			body: `{"variant":"x-high"}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelXHigh},
		},
		{
			name: "none",
			body: `{"variant":"none"}`,
			want: ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name: "auto",
			body: `{"variant":"auto"}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelAuto},
		},
		{
			name: "unknown",
			body: `{"variant":"mystery"}`,
			want: ThinkingConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractCodexConfig([]byte(tt.body))
			if got != tt.want {
				t.Fatalf("got=%+v want=%+v", got, tt.want)
			}
		})
	}
}
