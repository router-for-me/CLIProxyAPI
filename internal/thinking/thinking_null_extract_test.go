package thinking

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestExtractThinkingConfig_NullAndEmptyFields(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
		want     ThinkingConfig
	}{
		{
			name:     "gemini null thinkingLevel ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":null}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini empty thinkingLevel ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"   "}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini null thinkingBudget ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingBudget":null}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini null thinkingLevel falls through to numeric thinkingBudget",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":null,"thinkingBudget":2048}}}`,
			want:     ThinkingConfig{Mode: ModeBudget, Budget: 2048},
		},
		{
			name:     "interactions null thinking_level ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_level":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions empty thinking_level ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_level":""}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions null thinking_budget ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_budget":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions null thinking_level falls through to numeric thinking_budget",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_level":null,"thinking_budget":4096}}`,
			want:     ThinkingConfig{Mode: ModeBudget, Budget: 4096},
		},
		{
			name:     "openai null reasoning_effort ignored",
			provider: "openai",
			body:     `{"reasoning_effort":null}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "openai empty reasoning_effort ignored",
			provider: "openai",
			body:     `{"reasoning_effort":"  "}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex null reasoning.effort ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex empty reasoning.effort ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":""}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "claude null budget_tokens ignored",
			provider: "claude",
			body:     `{"thinking":{"budget_tokens":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "claude enabled with null budget_tokens falls back to auto",
			provider: "claude",
			body:     `{"thinking":{"type":"enabled","budget_tokens":null}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "gemini uppercase NONE level normalized",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"NONE"}}}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "gemini uppercase AUTO level normalized",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"AUTO"}}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ThinkingConfig
			switch tt.provider {
			case "gemini":
				got = extractGeminiConfig([]byte(tt.body), "gemini")
			case "interactions":
				got = extractInteractionsConfig([]byte(tt.body))
			case "openai":
				got = extractOpenAIConfig([]byte(tt.body))
			case "codex":
				got = extractCodexConfig([]byte(tt.body))
			case "claude":
				got = extractClaudeConfig([]byte(tt.body))
			default:
				t.Fatalf("unknown provider %q", tt.provider)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidateConfig_CaseInsensitiveNoneAndAuto(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID: "gemini-3-pro",
		Thinking: &registry.ThinkingSupport{
			Levels:         []string{"low", "medium", "high"},
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}

	tests := []struct {
		name     string
		level    ThinkingLevel
		wantMode ThinkingMode
	}{
		{
			name:     "uppercase NONE",
			level:    ThinkingLevel("NONE"),
			wantMode: ModeNone,
		},
		{
			name:     "mixed case Auto with spaces",
			level:    ThinkingLevel(" Auto "),
			wantMode: ModeAuto,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateConfig(ThinkingConfig{Mode: ModeLevel, Level: tt.level}, modelInfo, "gemini", "gemini", false)
			if err != nil {
				t.Fatalf("ValidateConfig(%q) returned unexpected error: %v", tt.level, err)
			}
			if got == nil {
				t.Fatalf("ValidateConfig(%q) returned nil config", tt.level)
			}
			if got.Mode != tt.wantMode {
				t.Fatalf("ValidateConfig(%q) mode = %v, want %v (config: %+v)", tt.level, got.Mode, tt.wantMode, *got)
			}
		})
	}
}
