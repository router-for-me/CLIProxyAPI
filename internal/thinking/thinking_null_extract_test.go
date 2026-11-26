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
			name:     "claude_enabled_null_budget_falls_back_to_auto",
			provider: "claude",
			body:     `{"thinking":{"type":"enabled","budget_tokens":null}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "claude_enabled_null_budget_uses_output_config_effort",
			provider: "claude",
			body:     `{"thinking":{"type":"enabled","budget_tokens":null},"output_config":{"effort":"high"}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:     "claude_null_budget_without_type_is_ignored",
			provider: "claude",
			body:     `{"thinking":{"budget_tokens":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "claude_non_numeric_budget_is_ignored",
			provider: "claude",
			body:     `{"thinking":{"type":"enabled","budget_tokens":"invalid"}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "claude_null_thinking_object_falls_back_to_effort",
			provider: "claude",
			body:     `{"thinking":null,"output_config":{"effort":"high"}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:     "codex_null_effort_is_ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex_empty_effort_is_ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":""}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex_whitespace_effort_is_ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":"   "}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex_non_string_effort_is_ignored",
			provider: "codex",
			body:     `{"reasoning":{"effort":123}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "codex_uppercase_none_normalized",
			provider: "codex",
			body:     `{"reasoning":{"effort":"NONE"}}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "codex_padded_none_normalized",
			provider: "codex",
			body:     `{"reasoning":{"effort":" none "}}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "codex_uppercase_auto_normalized",
			provider: "codex",
			body:     `{"reasoning":{"effort":"AUTO"}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "codex_padded_auto_normalized",
			provider: "codex",
			body:     `{"reasoning":{"effort":" auto "}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "codex_padded_uppercase_level_normalized",
			provider: "codex",
			body:     `{"reasoning":{"effort":" HIGH "}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:     "openai_null_effort_is_ignored",
			provider: "openai",
			body:     `{"reasoning_effort":null}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "openai_empty_effort_is_ignored",
			provider: "openai",
			body:     `{"reasoning_effort":""}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "openai_whitespace_effort_is_ignored",
			provider: "openai",
			body:     `{"reasoning_effort":"   "}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "openai_non_string_effort_is_ignored",
			provider: "openai",
			body:     `{"reasoning_effort":123}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "openai_null_effort_falls_back_to_google_thinking_config",
			provider: "openai",
			body:     `{"reasoning_effort":null,"extra_body":{"google":{"thinking_config":{"thinking_level":"medium"}}}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelMedium},
		},
		{
			name:     "openai_uppercase_none_normalized",
			provider: "openai",
			body:     `{"reasoning_effort":"NONE"}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "openai_padded_none_normalized",
			provider: "openai",
			body:     `{"reasoning_effort":" none "}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "openai_uppercase_auto_normalized",
			provider: "openai",
			body:     `{"reasoning_effort":"AUTO"}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "openai_padded_auto_normalized",
			provider: "openai",
			body:     `{"reasoning_effort":" auto "}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "openai_padded_uppercase_level_normalized",
			provider: "openai",
			body:     `{"reasoning_effort":" HIGH "}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:     "gemini_null_level_is_ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":null}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini_whitespace_level_is_ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"   "}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini_null_budget_is_ignored",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingBudget":null}}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "gemini_uppercase_none_normalized",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"NONE"}}}`,
			want:     ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:     "gemini_padded_uppercase_auto_normalized",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":" AUTO "}}}`,
			want:     ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:     "gemini_padded_uppercase_level_normalized_lowercase",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":" HIGH "}}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:     "gemini_null_level_falls_back_to_numeric_budget",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":null,"thinkingBudget":4096}}}`,
			want:     ThinkingConfig{Mode: ModeBudget, Budget: 4096},
		},
		{
			name:     "interactions_null_level_is_ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_level":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions_whitespace_level_is_ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_level":"   "}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions_null_budget_is_ignored",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_budget":null}}`,
			want:     ThinkingConfig{},
		},
		{
			name:     "interactions_integer_string_budget_preserved",
			provider: "interactions",
			body:     `{"generation_config":{"thinking_budget":"2048"}}`,
			want:     ThinkingConfig{Mode: ModeBudget, Budget: 2048},
		},
		{
			name:     "openai_custom_vendor_level_preserves_casing",
			provider: "openai",
			body:     `{"reasoning_effort":"VendorFast"}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: "VendorFast"},
		},
		{
			name:     "codex_custom_vendor_level_preserves_casing",
			provider: "codex",
			body:     `{"reasoning":{"effort":" VendorFast "}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: "VendorFast"},
		},
		{
			name:     "gemini_custom_vendor_level_preserves_casing",
			provider: "gemini",
			body:     `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"VendorFast"}}}`,
			want:     ThinkingConfig{Mode: ModeLevel, Level: "VendorFast"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractThinkingConfig([]byte(tc.body), tc.provider)
			if got != tc.want {
				t.Fatalf("extractThinkingConfig(%s, %s) = %+v, want %+v", tc.provider, tc.body, got, tc.want)
			}
		})
	}
}

func TestValidateConfig_CaseInsensitiveNoneAndAuto(t *testing.T) {
	levelModel := &registry.ModelInfo{
		ID:   "gpt-test-level",
		Type: "openai",
		Thinking: &registry.ThinkingSupport{
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"low", "medium", "high"},
		},
	}
	budgetModelNoDynamic := &registry.ModelInfo{
		ID:   "claude-test-budget-static",
		Type: "claude",
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: false,
		},
	}
	budgetModelDynamic := &registry.ModelInfo{
		ID:   "gemini-test-budget-dynamic",
		Type: "gemini",
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}

	tests := []struct {
		name       string
		config     ThinkingConfig
		modelInfo  *registry.ModelInfo
		fromFormat string
		toFormat   string
		want       ThinkingConfig
	}{
		{
			name:       "level_model_uppercase_none",
			config:     ThinkingConfig{Mode: ModeLevel, Level: "NONE"},
			modelInfo:  levelModel,
			fromFormat: "openai",
			toFormat:   "openai",
			want:       ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:       "level_model_padded_none",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " none "},
			modelInfo:  levelModel,
			fromFormat: "openai",
			toFormat:   "openai",
			want:       ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:       "level_model_uppercase_auto",
			config:     ThinkingConfig{Mode: ModeLevel, Level: "AUTO"},
			modelInfo:  levelModel,
			fromFormat: "openai",
			toFormat:   "openai",
			want:       ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:       "level_model_padded_auto",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " auto "},
			modelInfo:  levelModel,
			fromFormat: "openai",
			toFormat:   "openai",
			want:       ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:       "level_model_padded_uppercase_high",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " HIGH "},
			modelInfo:  levelModel,
			fromFormat: "openai",
			toFormat:   "openai",
			want:       ThinkingConfig{Mode: ModeLevel, Level: LevelHigh},
		},
		{
			name:       "budget_model_no_dynamic_uppercase_auto_converts_to_midrange",
			config:     ThinkingConfig{Mode: ModeLevel, Level: "AUTO"},
			modelInfo:  budgetModelNoDynamic,
			fromFormat: "claude",
			toFormat:   "claude",
			want:       ThinkingConfig{Mode: ModeBudget, Budget: 16896},
		},
		{
			name:       "budget_model_no_dynamic_padded_auto_converts_to_midrange",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " auto "},
			modelInfo:  budgetModelNoDynamic,
			fromFormat: "claude",
			toFormat:   "claude",
			want:       ThinkingConfig{Mode: ModeBudget, Budget: 16896},
		},
		{
			name:       "budget_model_padded_none",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " none "},
			modelInfo:  budgetModelNoDynamic,
			fromFormat: "claude",
			toFormat:   "claude",
			want:       ThinkingConfig{Mode: ModeNone, Budget: 0},
		},
		{
			name:       "budget_model_padded_uppercase_high",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " HIGH "},
			modelInfo:  budgetModelNoDynamic,
			fromFormat: "claude",
			toFormat:   "claude",
			want:       ThinkingConfig{Mode: ModeBudget, Budget: 24576},
		},
		{
			name:       "budget_model_dynamic_uppercase_auto",
			config:     ThinkingConfig{Mode: ModeLevel, Level: "AUTO"},
			modelInfo:  budgetModelDynamic,
			fromFormat: "gemini",
			toFormat:   "gemini",
			want:       ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
		{
			name:       "budget_model_dynamic_padded_auto",
			config:     ThinkingConfig{Mode: ModeLevel, Level: " auto "},
			modelInfo:  budgetModelDynamic,
			fromFormat: "gemini",
			toFormat:   "gemini",
			want:       ThinkingConfig{Mode: ModeAuto, Budget: -1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateConfig(tc.config, tc.modelInfo, tc.fromFormat, tc.toFormat, false)
			if err != nil {
				t.Fatalf("ValidateConfig(%+v) error = %v", tc.config, err)
			}
			if got == nil {
				t.Fatalf("ValidateConfig(%+v) returned nil", tc.config)
			}
			if *got != tc.want {
				t.Fatalf("ValidateConfig(%+v) = %+v, want %+v", tc.config, *got, tc.want)
			}
		})
	}
}
