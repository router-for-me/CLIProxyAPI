package antigravity_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
)

func claudeSonnet46AntigravityModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:                  "claude-sonnet-4-6",
		Type:                "antigravity",
		MaxCompletionTokens: 64000,
		Thinking: &registry.ThinkingSupport{
			Min:            1024,
			Max:            64000,
			ZeroAllowed:    true,
			DynamicAllowed: true,
		},
	}
}

func TestApplier_ClaudeInvalidBudgetDoesNotRecreateOrphanIncludeThoughts(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}
	modelInfo := claudeSonnet46AntigravityModel()

	tests := []struct {
		name   string
		body   string
		config thinking.ThinkingConfig
	}{
		{
			name:   "budget_below_min_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "budget_below_min_with_snake_case_include_thoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"include_thoughts":true,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "budget_below_min_with_includeThoughts_false",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":false,"thinkingBudget":512}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
		{
			name:   "maxOutputTokens_clamps_budget_below_min_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048},
		},
		{
			name:   "mode_none_budget_zero_with_includeThoughts_true",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingBudget":0}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0},
		},
		{
			name:   "mode_none_budget_zero_with_includeThoughts_false",
			body:   `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":false,"thinkingBudget":0}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0},
		},
		{
			name:   "maxOutputTokens_clamps_below_min_with_snake_case_thinking_config_include_thoughts",
			body:   `{"request":{"generationConfig":{"maxOutputTokens":1024,"thinking_config":{"include_thoughts":true}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048},
		},
		{
			name:   "budget_below_min_with_snake_case_thinking_config_includeThoughts",
			body:   `{"request":{"generationConfig":{"thinking_config":{"includeThoughts":true}}}}`,
			config: thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errApply := applier.Apply([]byte(tc.body), tc.config, modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
				t.Fatalf("expected thinkingConfig to be absent when Claude budget is stripped, got %s in body=%s", tcNode.Raw, out)
			}
			if tcSnake := ojson.GetBytes(out, "request.generationConfig.thinking_config"); tcSnake.Exists() {
				t.Fatalf("expected thinking_config to be absent when Claude budget is stripped, got %s in body=%s", tcSnake.Raw, out)
			}
		})
	}
}

func TestApplier_ClaudeValidBudgetPreservesIncludeThoughts(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}
	modelInfo := claudeSonnet46AntigravityModel()

	body := []byte(`{"request":{"generationConfig":{"maxOutputTokens":2048,"thinkingConfig":{"include_thoughts":true,"thinkingBudget":4096}}}}`)
	out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096}, modelInfo)
	if errApply != nil {
		t.Fatalf("Apply() error = %v", errApply)
	}
	if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2047 {
		t.Fatalf("thinkingBudget = %d, want 2047; body=%s", got, out)
	}
	if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
		t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
	}
	if ojson.GetBytes(out, "request.generationConfig.thinkingConfig.include_thoughts").Exists() {
		t.Fatalf("expected snake_case include_thoughts to be normalized; body=%s", out)
	}
}

func TestApplier_NormalizesSnakeCaseThinkingConfig(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}

	tests := []struct {
		name      string
		modelInfo *registry.ModelInfo
		config    thinking.ThinkingConfig
		wantPath  string
		wantValue string
	}{
		{
			name:      "budget",
			modelInfo: claudeSonnet46AntigravityModel(),
			config:    thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096},
			wantPath:  "request.generationConfig.thinkingConfig.thinkingBudget",
			wantValue: "4096",
		},
		{
			name:      "level",
			modelInfo: gemini4NeonAntigravityModel(),
			config:    thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh},
			wantPath:  "request.generationConfig.thinkingConfig.thinkingLevel",
			wantValue: "HIGH",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"request":{"generationConfig":{"thinking_config":{"include_thoughts":true,"thinking_budget":2048}}}}`)
			out, errApply := applier.Apply(body, tc.config, tc.modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if stale := ojson.GetBytes(out, "request.generationConfig.thinking_config"); stale.Exists() {
				t.Fatalf("thinking_config remained alongside thinkingConfig: %s", out)
			}
			if got := ojson.GetBytes(out, tc.wantPath).String(); got != tc.wantValue {
				t.Fatalf("%s = %q, want %q; body=%s", tc.wantPath, got, tc.wantValue, out)
			}
			if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
				t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
			}
		})
	}
}

func gemini4NeonAntigravityModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:   "gemini-4-neon-xhigh",
		Type: "antigravity",
		Thinking: &registry.ThinkingSupport{
			Min:            1,
			Max:            65535,
			DynamicAllowed: true,
			Levels:         []string{"minimal", "low", "medium", "high", "xhigh"},
		},
	}
}

func TestApplier_EmitsUppercaseThinkingLevelEnumNames(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}

	levelModel := gemini4NeonAntigravityModel()
	userDefinedModel := &registry.ModelInfo{
		ID:          "custom-antigravity-gemini-4",
		Type:        "antigravity",
		UserDefined: true,
	}

	tests := []struct {
		name       string
		modelInfo  *registry.ModelInfo
		body       string
		config     thinking.ThinkingConfig
		wantLevel  string
		wantEffort string
	}{
		{
			name:       "mode_level_minimal",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinking_level":"minimal"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMinimal},
			wantLevel:  "MINIMAL",
			wantEffort: "minimal",
		},
		{
			name:       "mode_level_low",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"low"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelLow},
			wantLevel:  "LOW",
			wantEffort: "low",
		},
		{
			name:       "mode_level_medium",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"medium"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMedium},
			wantLevel:  "MEDIUM",
			wantEffort: "medium",
		},
		{
			name:       "mode_level_high",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh},
			wantLevel:  "HIGH",
			wantEffort: "high",
		},
		{
			name:       "mode_level_xhigh",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"xhigh"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelXHigh},
			wantLevel:  "XHIGH",
			wantEffort: "xhigh",
		},
		{
			name:       "mode_none_fallback_level_low",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"none"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.LevelLow},
			wantLevel:  "LOW",
			wantEffort: "low",
		},
		{
			name:       "user_defined_model_mode_level_xhigh",
			modelInfo:  userDefinedModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"xhigh"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelXHigh},
			wantLevel:  "XHIGH",
			wantEffort: "xhigh",
		},
		{
			name:       "already_uppercase_and_trimmed_idempotent",
			modelInfo:  levelModel,
			body:       `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH"}}}}`,
			config:     thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.ThinkingLevel(" high ")},
			wantLevel:  "HIGH",
			wantEffort: "high",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errApply := applier.Apply([]byte(tc.body), tc.config, tc.modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != tc.wantLevel {
				t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, tc.wantLevel, out)
			}
			if ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinking_level").Exists() {
				t.Fatalf("expected snake_case thinking_level to be deleted; body=%s", out)
			}
			if ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Exists() {
				t.Fatalf("unexpected thinkingBudget in level output; body=%s", out)
			}
			if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
				t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
			}
			if effort := thinking.ExtractTranslatedReasoningEffort(out, "antigravity"); effort != tc.wantEffort {
				t.Fatalf("ExtractTranslatedReasoningEffort() = %q, want %q; body=%s", effort, tc.wantEffort, out)
			}
			secondOut, errSecond := applier.Apply(out, tc.config, tc.modelInfo)
			if errSecond != nil {
				t.Fatalf("second Apply() error = %v", errSecond)
			}
			if got := ojson.GetBytes(secondOut, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != tc.wantLevel {
				t.Fatalf("second Apply() thinkingLevel = %q, want %q; body=%s", got, tc.wantLevel, secondOut)
			}
		})
	}

	t.Run("mode_none_whitespace_level_deletes_thinkingConfig", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone, Level: thinking.ThinkingLevel("   ")}, levelModel)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
			t.Fatalf("expected thinkingConfig to be deleted for whitespace-only ModeNone level, got %s in body=%s", tcNode.Raw, out)
		}
	})
}

func TestApplier_ClaudeEffectiveMaxTokensSnakeCase(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}
	modelInfo := claudeSonnet46AntigravityModel()

	t.Run("strips_thinking_for_snake_case_max_output_tokens_at_or_below_1024", func(t *testing.T) {
		tests := []struct {
			name string
			body string
		}{
			{
				name: "generationConfig_max_output_tokens_1024",
				body: `{"request":{"generationConfig":{"max_output_tokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			},
			{
				name: "generationConfig_max_output_tokens_512",
				body: `{"request":{"generationConfig":{"max_output_tokens":512,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			},
			{
				name: "generation_config_max_output_tokens_512",
				body: `{"request":{"generation_config":{"max_output_tokens":512,"thinking_config":{"include_thoughts":true,"thinking_budget":2048}}}}`,
			},
			{
				name: "non_numeric_maxOutputTokens_falls_back_to_numeric_max_output_tokens",
				body: `{"request":{"generationConfig":{"maxOutputTokens":"4096","max_output_tokens":512,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			},
			{
				name: "both_camel_and_snake_uses_smaller_limit",
				body: `{"request":{"generationConfig":{"maxOutputTokens":4096,"max_output_tokens":1024,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":2048}}}}`,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				out, errApply := applier.Apply([]byte(tc.body), thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048}, modelInfo)
				if errApply != nil {
					t.Fatalf("Apply() error = %v", errApply)
				}
				if tcNode := ojson.GetBytes(out, "request.generationConfig.thinkingConfig"); tcNode.Exists() {
					t.Fatalf("expected thinkingConfig to be stripped, got %s; body=%s", tcNode.Raw, out)
				}
				if tcSnake := ojson.GetBytes(out, "request.generationConfig.thinking_config"); tcSnake.Exists() {
					t.Fatalf("expected thinking_config to be stripped, got %s; body=%s", tcSnake.Raw, out)
				}
				if got := ojson.GetBytes(out, "request.generationConfig.maxOutputTokens"); got.Exists() && got.Type == ojson.Number && got.Int() == int64(modelInfo.MaxCompletionTokens) {
					t.Fatalf("unexpected default maxOutputTokens=%d injected when caller provided max_output_tokens; body=%s", got.Int(), out)
				}
			})
		}
	})

	t.Run("clamps_budget_using_snake_case_max_output_tokens_2048", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"max_output_tokens":2048,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":4096}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096}, modelInfo)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2047 {
			t.Fatalf("thinkingBudget = %d, want 2047; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.maxOutputTokens"); got.Exists() {
			t.Fatalf("expected default maxOutputTokens not to be injected when max_output_tokens is set, got %s; body=%s", got.Raw, out)
		}
	})

	t.Run("caps_caller_max_output_tokens_above_model_limit", func(t *testing.T) {
		body := []byte(`{"request":{"generationConfig":{"max_output_tokens":70000,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":64000}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 64000}, modelInfo)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 63999 {
			t.Fatalf("thinkingBudget = %d, want 63999; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
	})
}
