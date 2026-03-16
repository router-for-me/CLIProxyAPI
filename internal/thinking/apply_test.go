package thinking

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractClaudeConfig_AdaptiveBudgetTokensFallback(t *testing.T) {
	tests := []struct {
		name string
		body string
		want ThinkingConfig
	}{
		{
			name: "adaptive with budget_tokens falls back to budget",
			body: `{"thinking":{"type":"adaptive","budget_tokens":10000}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 10000},
		},
		{
			name: "auto with budget_tokens falls back to budget",
			body: `{"thinking":{"type":"auto","budget_tokens":8192}}`,
			want: ThinkingConfig{Mode: ModeBudget, Budget: 8192},
		},
		{
			name: "adaptive with budget_tokens zero returns none",
			body: `{"thinking":{"type":"adaptive","budget_tokens":0}}`,
			want: ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name: "adaptive with budget_tokens -1 returns auto",
			body: `{"thinking":{"type":"adaptive","budget_tokens":-1}}`,
			want: ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name: "adaptive with effort takes precedence over budget_tokens",
			body: `{"thinking":{"type":"adaptive","budget_tokens":10000},"output_config":{"effort":"high"}}`,
			want: ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name: "adaptive alone returns empty config (passthrough)",
			body: `{"thinking":{"type":"adaptive"}}`,
			want: ThinkingConfig{},
		},
		{
			name: "auto alone returns empty config (passthrough)",
			body: `{"thinking":{"type":"auto"}}`,
			want: ThinkingConfig{},
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

func TestApplyThinking_AdaptiveBudgetTokensNormalized(t *testing.T) {
	// End-to-end: verify that adaptive+budget_tokens input is normalized to
	// enabled+budget_tokens (a valid Claude API combination) instead of being
	// passed through as the invalid adaptive+budget_tokens combination.
	body := []byte(`{"thinking":{"type":"adaptive","budget_tokens":10000},"model":"claude-sonnet-4-6","max_tokens":16384}`)
	result, err := ApplyThinking(body, "claude-sonnet-4-6", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	// thinking.type must be "enabled" — the budget was extracted as ModeBudget
	// and the applier converts that to manual thinking with the given budget.
	thinkingType := gjson.GetBytes(result, "thinking.type").String()
	if thinkingType != "enabled" {
		t.Fatalf("expected thinking.type=enabled, got %q in: %s", thinkingType, string(result))
	}
	// budget_tokens is valid with type="enabled", so it should be present.
	if !gjson.GetBytes(result, "thinking.budget_tokens").Exists() {
		t.Fatalf("budget_tokens should be present with type=enabled: %s", string(result))
	}
}

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
