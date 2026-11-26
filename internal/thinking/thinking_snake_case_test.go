package thinking_test

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/interactions"
)

func testGeminiLevelModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:   "gemini-3-pro",
		Type: "gemini",
		Thinking: &registry.ThinkingSupport{
			Min:            0,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"minimal", "low", "medium", "high"},
		},
	}
}

func testGeminiBudgetModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:   "gemini-2.5-pro",
		Type: "gemini",
		Thinking: &registry.ThinkingSupport{
			Min:            128,
			Max:            32768,
			ZeroAllowed:    false,
			DynamicAllowed: true,
		},
	}
}

func testNonThinkingModel(id, providerType string) *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:   id,
		Type: providerType,
	}
}

func testInteractionsLevelModel() *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:   "gemini-3-pro",
		Type: "gemini",
		Thinking: &registry.ThinkingSupport{
			Min:            0,
			Max:            32768,
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"minimal", "low", "medium", "high"},
		},
	}
}

func TestGeminiApplyDoc_SnakeCaseContainersAndIncludeThoughts(t *testing.T) {
	applier := thinking.GetProviderApplier("gemini")
	if applier == nil {
		t.Fatal("gemini provider applier is not registered")
	}

	levelModel := testGeminiLevelModel()
	budgetModel := testGeminiBudgetModel()

	tests := []struct {
		name            string
		modelInfo       *registry.ModelInfo
		body            string
		config          thinking.ThinkingConfig
		wantPath        string
		wantValue       string
		wantInclude     bool
		wantHasInclude  bool
		wantKeepTemp    bool
		wantDropGenConf bool
	}{
		{
			name:            "level_from_generationConfig_thinking_config",
			modelInfo:       levelModel,
			body:            `{"generationConfig":{"thinking_config":{"thinking_level":"high","include_thoughts":true}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh},
			wantPath:        "generationConfig.thinkingConfig.thinkingLevel",
			wantValue:       "high",
			wantInclude:     true,
			wantHasInclude:  true,
			wantDropGenConf: true,
		},
		{
			name:            "level_from_generation_config_thinkingConfig_preserves_non_thinking",
			modelInfo:       levelModel,
			body:            `{"generation_config":{"temperature":0.7,"thinkingConfig":{"thinkingLevel":"low","includeThoughts":false}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelLow},
			wantPath:        "generationConfig.thinkingConfig.thinkingLevel",
			wantValue:       "low",
			wantInclude:     false,
			wantHasInclude:  true,
			wantKeepTemp:    true,
			wantDropGenConf: false,
		},
		{
			name:            "level_from_generation_config_thinking_config_drops_empty_container",
			modelInfo:       levelModel,
			body:            `{"generation_config":{"thinking_config":{"thinking_level":"medium","include_thoughts":true}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMedium},
			wantPath:        "generationConfig.thinkingConfig.thinkingLevel",
			wantValue:       "medium",
			wantInclude:     true,
			wantHasInclude:  true,
			wantDropGenConf: true,
		},
		{
			name:            "budget_from_generationConfig_thinking_config",
			modelInfo:       budgetModel,
			body:            `{"generationConfig":{"thinking_config":{"thinking_budget":2048,"include_thoughts":true}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048},
			wantPath:        "generationConfig.thinkingConfig.thinkingBudget",
			wantValue:       "2048",
			wantInclude:     true,
			wantHasInclude:  true,
			wantDropGenConf: true,
		},
		{
			name:            "budget_from_generation_config_thinking_config_preserves_non_thinking",
			modelInfo:       budgetModel,
			body:            `{"generation_config":{"temperature":0.4,"thinking_config":{"thinking_budget":4096,"includeThoughts":false}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 4096},
			wantPath:        "generationConfig.thinkingConfig.thinkingBudget",
			wantValue:       "4096",
			wantInclude:     false,
			wantHasInclude:  true,
			wantKeepTemp:    true,
			wantDropGenConf: false,
		},
		{
			name:            "mode_none_deletes_all_snake_and_mixed_containers",
			modelInfo:       levelModel,
			body:            `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"},"thinking_config":{"include_thoughts":true}},"generation_config":{"thinking_config":{"thinking_level":"low"}}}`,
			config:          thinking.ThinkingConfig{Mode: thinking.ModeNone, Budget: 0},
			wantPath:        "",
			wantHasInclude:  false,
			wantDropGenConf: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errApply := applier.Apply([]byte(tc.body), tc.config, tc.modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}

			for _, stalePath := range []string{
				"generationConfig.thinking_config",
				"generation_config.thinkingConfig",
				"generation_config.thinking_config",
			} {
				if got := ojson.GetBytes(out, stalePath); got.Exists() {
					t.Fatalf("expected stale path %s to be deleted, got %s in body=%s", stalePath, got.Raw, out)
				}
			}

			if tc.wantDropGenConf {
				if gc := ojson.GetBytes(out, "generation_config"); gc.Exists() {
					t.Fatalf("expected empty generation_config to be deleted, got %s in body=%s", gc.Raw, out)
				}
			}
			if tc.wantKeepTemp {
				if temp := ojson.GetBytes(out, "generation_config.temperature"); !temp.Exists() {
					t.Fatalf("expected generation_config.temperature to be preserved; body=%s", out)
				}
			}

			if tc.wantPath == "" {
				if tcNode := ojson.GetBytes(out, "generationConfig.thinkingConfig"); tcNode.Exists() {
					t.Fatalf("expected generationConfig.thinkingConfig to be deleted, got %s in body=%s", tcNode.Raw, out)
				}
				return
			}

			if got := ojson.GetBytes(out, tc.wantPath).String(); got != tc.wantValue {
				t.Fatalf("%s = %q, want %q; body=%s", tc.wantPath, got, tc.wantValue, out)
			}
			include := ojson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts")
			if include.Exists() != tc.wantHasInclude {
				t.Fatalf("includeThoughts exists = %v, want %v; body=%s", include.Exists(), tc.wantHasInclude, out)
			}
			if tc.wantHasInclude && include.Bool() != tc.wantInclude {
				t.Fatalf("includeThoughts = %v, want %v; body=%s", include.Bool(), tc.wantInclude, out)
			}
		})
	}
}

func TestAntigravityApplyDoc_GenerationConfigSnakeCaseContainers(t *testing.T) {
	applier := thinking.GetProviderApplier("antigravity")
	if applier == nil {
		t.Fatal("antigravity provider applier is not registered")
	}

	levelModel := &registry.ModelInfo{
		ID:   "gemini-4-neon-xhigh",
		Type: "antigravity",
		Thinking: &registry.ThinkingSupport{
			Min:            1,
			Max:            65535,
			DynamicAllowed: true,
			Levels:         []string{"minimal", "low", "medium", "high", "xhigh"},
		},
	}
	claudeModel := &registry.ModelInfo{
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

	t.Run("level_from_request_generation_config_thinking_config", func(t *testing.T) {
		body := []byte(`{"request":{"generation_config":{"thinking_config":{"thinking_level":"high","include_thoughts":true}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, levelModel)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "HIGH" {
			t.Fatalf("thinkingLevel = %q, want HIGH; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
		for _, stale := range []string{
			"request.generationConfig.thinking_config",
			"request.generation_config.thinkingConfig",
			"request.generation_config.thinking_config",
			"request.generation_config",
		} {
			if ojson.GetBytes(out, stale).Exists() {
				t.Fatalf("expected %s to be deleted; body=%s", stale, out)
			}
		}
	})

	t.Run("budget_from_request_generation_config_preserves_non_thinking", func(t *testing.T) {
		body := []byte(`{"request":{"generation_config":{"temperature":0.5,"thinkingConfig":{"thinking_budget":2048,"includeThoughts":false}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 2048}, claudeModel)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
			t.Fatalf("thinkingBudget = %d, want 2048; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || include.Bool() {
			t.Fatalf("includeThoughts = %s, want false; body=%s", include.Raw, out)
		}
		if ojson.GetBytes(out, "request.generation_config.thinkingConfig").Exists() {
			t.Fatalf("expected request.generation_config.thinkingConfig to be deleted; body=%s", out)
		}
		if !ojson.GetBytes(out, "request.generation_config.temperature").Exists() {
			t.Fatalf("expected request.generation_config.temperature to be preserved; body=%s", out)
		}
	})

	t.Run("claude_invalid_budget_strips_request_generation_config_thinking", func(t *testing.T) {
		body := []byte(`{"request":{"generation_config":{"thinking_config":{"include_thoughts":true,"thinking_budget":512}}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 512}, claudeModel)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		for _, path := range []string{
			"request.generationConfig.thinkingConfig",
			"request.generationConfig.thinking_config",
			"request.generation_config.thinkingConfig",
			"request.generation_config.thinking_config",
		} {
			if ojson.GetBytes(out, path).Exists() {
				t.Fatalf("expected %s to be deleted when Claude budget is invalid; body=%s", path, out)
			}
		}
	})
}

func TestInteractionsApplyDoc_CamelCaseGenerationConfigContainers(t *testing.T) {
	applier := thinking.GetProviderApplier("interactions")
	if applier == nil {
		t.Fatal("interactions provider applier is not registered")
	}

	modelInfo := testInteractionsLevelModel()

	t.Run("strips_generationConfig_thinking_config_and_preserves_include_thoughts", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"temperature":0.2,"thinking_config":{"thinking_level":"high","include_thoughts":true}}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelHigh}, modelInfo)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_level").String(); got != "high" {
			t.Fatalf("generation_config.thinking_level = %q, want high; body=%s", got, out)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_summaries").String(); got != "auto" {
			t.Fatalf("generation_config.thinking_summaries = %q, want auto; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig.thinking_config").Exists() {
			t.Fatalf("expected generationConfig.thinking_config to be deleted; body=%s", out)
		}
		if !ojson.GetBytes(out, "generationConfig.temperature").Exists() {
			t.Fatalf("expected generationConfig.temperature to be preserved; body=%s", out)
		}
	})

	t.Run("preserves_generationConfig_thinkingSummaries_and_drops_empty_generationConfig", func(t *testing.T) {
		body := []byte(`{"generationConfig":{"thinkingLevel":"low","thinkingSummaries":"none"}}`)
		out, errApply := applier.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelLow}, modelInfo)
		if errApply != nil {
			t.Fatalf("Apply() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_level").String(); got != "low" {
			t.Fatalf("generation_config.thinking_level = %q, want low; body=%s", got, out)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_summaries").String(); got != "none" {
			t.Fatalf("generation_config.thinking_summaries = %q, want none; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig").Exists() {
			t.Fatalf("expected empty generationConfig to be deleted; body=%s", out)
		}
	})
}

func TestStripThinkingConfig_SnakeCaseAndCamelCaseContainers(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		body        string
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:     "gemini_strips_all_snake_and_mixed_containers",
			provider: "gemini",
			body:     `{"generationConfig":{"temperature":0.5,"thinkingConfig":{"thinkingBudget":1024},"thinking_config":{"thinking_budget":1024}},"generation_config":{"max_output_tokens":256,"thinkingConfig":{"thinkingLevel":"high"},"thinking_config":{"thinking_level":"low"}}}`,
			wantAbsent: []string{
				"generationConfig.thinkingConfig",
				"generationConfig.thinking_config",
				"generation_config.thinkingConfig",
				"generation_config.thinking_config",
			},
			wantPresent: []string{
				"generationConfig.temperature",
				"generation_config.max_output_tokens",
			},
		},
		{
			name:     "antigravity_strips_all_snake_and_mixed_containers",
			provider: "antigravity",
			body:     `{"request":{"generationConfig":{"temperature":0.3,"thinkingConfig":{"thinkingBudget":1024},"thinking_config":{"thinking_budget":1024}},"generation_config":{"thinkingConfig":{"thinkingLevel":"high"},"thinking_config":{"thinking_level":"low"}}}}`,
			wantAbsent: []string{
				"request.generationConfig.thinkingConfig",
				"request.generationConfig.thinking_config",
				"request.generation_config.thinkingConfig",
				"request.generation_config.thinking_config",
				"request.generation_config",
			},
			wantPresent: []string{
				"request.generationConfig.temperature",
			},
		},
		{
			name:     "interactions_strips_camelCase_generationConfig_thinking_fields",
			provider: "interactions",
			body:     `{"generationConfig":{"temperature":0.2,"thinking_level":"high","thinkingLevel":"low","thinking_budget":1024,"thinkingBudget":2048,"thinking_summaries":"auto","thinkingSummaries":"none","thinking_config":{"include_thoughts":true},"thinkingConfig":{"includeThoughts":false}}}`,
			wantAbsent: []string{
				"generationConfig.thinking_level",
				"generationConfig.thinkingLevel",
				"generationConfig.thinking_budget",
				"generationConfig.thinkingBudget",
				"generationConfig.thinking_summaries",
				"generationConfig.thinkingSummaries",
				"generationConfig.thinking_config",
				"generationConfig.thinkingConfig",
			},
			wantPresent: []string{
				"generationConfig.temperature",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := thinking.StripThinkingConfig([]byte(tc.body), tc.provider)
			for _, path := range tc.wantAbsent {
				if got := ojson.GetBytes(out, path); got.Exists() {
					t.Fatalf("expected %s to be stripped, got %s in body=%s", path, got.Raw, out)
				}
			}
			for _, path := range tc.wantPresent {
				if !ojson.GetBytes(out, path).Exists() {
					t.Fatalf("expected non-thinking field %s to be preserved; body=%s", path, out)
				}
			}
		})
	}
}

func TestApplyThinking_SnakeCaseAndCamelCaseEndToEnd(t *testing.T) {
	t.Run("gemini_extracts_and_normalizes_snake_case_thinking_budget_and_level", func(t *testing.T) {
		budgetModel := testGeminiBudgetModel()
		budgetBody := []byte(`{"generation_config":{"temperature":0.5,"thinking_config":{"thinking_budget":2048,"include_thoughts":true}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(budgetBody, budgetBody, budgetModel.ID, "gemini", "gemini", "gemini", budgetModel)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo(budget) error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
			t.Fatalf("thinkingBudget = %d, want 2048; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
		if ojson.GetBytes(out, "generation_config.thinking_config").Exists() {
			t.Fatalf("expected generation_config.thinking_config to be deleted; body=%s", out)
		}
		if !ojson.GetBytes(out, "generation_config.temperature").Exists() {
			t.Fatalf("expected generation_config.temperature to be preserved; body=%s", out)
		}

		levelModel := testGeminiLevelModel()
		levelBody := []byte(`{"generationConfig":{"thinking_config":{"thinking_level":"high","include_thoughts":false}}}`)
		outLevel, errLevel := thinking.ApplyThinkingWithModelInfo(levelBody, levelBody, levelModel.ID, "gemini", "gemini", "gemini", levelModel)
		if errLevel != nil {
			t.Fatalf("ApplyThinkingWithModelInfo(level) error = %v", errLevel)
		}
		if got := ojson.GetBytes(outLevel, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
			t.Fatalf("thinkingLevel = %q, want high; body=%s", got, outLevel)
		}
		if include := ojson.GetBytes(outLevel, "generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || include.Bool() {
			t.Fatalf("includeThoughts = %s, want false; body=%s", include.Raw, outLevel)
		}
		if ojson.GetBytes(outLevel, "generationConfig.thinking_config").Exists() {
			t.Fatalf("expected generationConfig.thinking_config to be deleted; body=%s", outLevel)
		}
	})

	t.Run("antigravity_extracts_and_normalizes_snake_case_request_generation_config", func(t *testing.T) {
		levelModel := &registry.ModelInfo{
			ID:   "gemini-4-neon-xhigh",
			Type: "antigravity",
			Thinking: &registry.ThinkingSupport{
				Min:            1,
				Max:            65535,
				DynamicAllowed: true,
				Levels:         []string{"minimal", "low", "medium", "high", "xhigh"},
			},
		}
		body := []byte(`{"request":{"generation_config":{"thinking_config":{"thinking_level":"high","include_thoughts":true}}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, levelModel.ID, "antigravity", "antigravity", "antigravity", levelModel)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "HIGH" {
			t.Fatalf("thinkingLevel = %q, want HIGH; body=%s", got, out)
		}
		if include := ojson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
			t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, out)
		}
		if ojson.GetBytes(out, "request.generation_config").Exists() {
			t.Fatalf("expected empty request.generation_config to be deleted; body=%s", out)
		}
	})

	t.Run("gemini_non_thinking_model_strips_snake_case_containers", func(t *testing.T) {
		model := testNonThinkingModel("gemini-2.0-flash", "gemini")
		body := []byte(`{"generation_config":{"thinking_config":{"thinking_budget":1024,"include_thoughts":true}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "gemini", "gemini", "gemini", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "generation_config.thinking_config").Exists() || ojson.GetBytes(out, "generation_config").Exists() {
			t.Fatalf("expected snake_case thinking config to be stripped for non-thinking model; body=%s", out)
		}
	})

	t.Run("antigravity_non_thinking_model_strips_snake_case_containers", func(t *testing.T) {
		model := testNonThinkingModel("gemini-2.0-flash", "antigravity")
		body := []byte(`{"request":{"generation_config":{"thinking_config":{"thinking_budget":1024,"include_thoughts":true}}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "antigravity", "antigravity", "antigravity", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "request.generation_config.thinking_config").Exists() || ojson.GetBytes(out, "request.generation_config").Exists() {
			t.Fatalf("expected snake_case thinking config to be stripped for non-thinking antigravity model; body=%s", out)
		}
	})

	t.Run("interactions_extracts_and_normalizes_camelCase_generationConfig", func(t *testing.T) {
		model := testInteractionsLevelModel()
		body := []byte(`{"generationConfig":{"thinking_config":{"thinking_level":"high","include_thoughts":true}}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "interactions", "interactions", "gemini", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_level").String(); got != "high" {
			t.Fatalf("generation_config.thinking_level = %q, want high; body=%s", got, out)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_summaries").String(); got != "auto" {
			t.Fatalf("generation_config.thinking_summaries = %q, want auto; body=%s", got, out)
		}
		if ojson.GetBytes(out, "generationConfig").Exists() {
			t.Fatalf("expected empty generationConfig to be deleted; body=%s", out)
		}
	})

	t.Run("interactions_non_thinking_model_strips_camelCase_generationConfig", func(t *testing.T) {
		model := testNonThinkingModel("gemini-2.0-flash", "gemini")
		body := []byte(`{"generationConfig":{"temperature":0.6,"thinkingLevel":"high","thinkingSummaries":"auto"}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "interactions", "interactions", "gemini", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
		}
		if ojson.GetBytes(out, "generationConfig.thinkingLevel").Exists() || ojson.GetBytes(out, "generationConfig.thinkingSummaries").Exists() {
			t.Fatalf("expected camelCase generationConfig thinking fields to be stripped; body=%s", out)
		}
		if !ojson.GetBytes(out, "generationConfig.temperature").Exists() {
			t.Fatalf("expected generationConfig.temperature to be preserved; body=%s", out)
		}
	})

	t.Run("non_thinking_models_strip_empty_alias_containers_via_ApplyThinkingWithModelInfo", func(t *testing.T) {
		cases := []struct {
			name        string
			format      string
			providerKey string
			body        string
			wantAbsent  []string
			wantPresent []string
		}{
			{
				name:        "gemini_empty_snake_thinking_config",
				format:      "gemini",
				providerKey: "gemini",
				body:        `{"generation_config":{"thinking_config":{}}}`,
				wantAbsent:  []string{"generation_config.thinking_config", "generation_config"},
			},
			{
				name:        "gemini_empty_snake_thinking_config_preserves_temperature",
				format:      "gemini",
				providerKey: "gemini",
				body:        `{"generation_config":{"temperature":0.4,"thinking_config":{}}}`,
				wantAbsent:  []string{"generation_config.thinking_config"},
				wantPresent: []string{"generation_config.temperature"},
			},
			{
				name:        "antigravity_empty_request_generation_config_thinking_config",
				format:      "antigravity",
				providerKey: "antigravity",
				body:        `{"request":{"generation_config":{"thinking_config":{}}}}`,
				wantAbsent:  []string{"request.generation_config.thinking_config", "request.generation_config"},
			},
			{
				name:        "interactions_empty_camelCase_generationConfig_thinkingConfig",
				format:      "interactions",
				providerKey: "gemini",
				body:        `{"generationConfig":{"thinkingConfig":{}}}`,
				wantAbsent:  []string{"generationConfig.thinkingConfig", "generationConfig"},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				model := testNonThinkingModel("gemini-2.0-flash", tc.providerKey)
				out, errApply := thinking.ApplyThinkingWithModelInfo([]byte(tc.body), []byte(tc.body), model.ID, tc.format, tc.format, tc.providerKey, model)
				if errApply != nil {
					t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
				}
				for _, path := range tc.wantAbsent {
					if got := ojson.GetBytes(out, path); got.Exists() {
						t.Fatalf("expected %s to be stripped, got %s in body=%s", path, got.Raw, out)
					}
				}
				for _, path := range tc.wantPresent {
					if !ojson.GetBytes(out, path).Exists() {
						t.Fatalf("expected %s to be preserved; body=%s", path, out)
					}
				}
			})
		}
	})

	t.Run("interactions_integer_string_budget_preserved_and_invalid_skipped", func(t *testing.T) {
		model := testInteractionsLevelModel()

		body := []byte(`{"generation_config":{"thinking_budget":"2048"}}`)
		out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "interactions", "interactions", "gemini", model)
		if errApply != nil {
			t.Fatalf("ApplyThinkingWithModelInfo(string budget) error = %v", errApply)
		}
		if got := ojson.GetBytes(out, "generation_config.thinking_level").String(); got == "" {
			t.Fatalf("expected generation_config.thinking_level to be populated from integer string budget; body=%s", out)
		}
		if ojson.GetBytes(out, "generation_config.thinking_budget").Exists() {
			t.Fatalf("expected generation_config.thinking_budget to be normalized away; body=%s", out)
		}

		camelBody := []byte(`{"generationConfig":{"thinkingBudget":"4096"}}`)
		outCamel, errCamel := thinking.ApplyThinkingWithModelInfo(camelBody, camelBody, model.ID, "interactions", "interactions", "gemini", model)
		if errCamel != nil {
			t.Fatalf("ApplyThinkingWithModelInfo(camelCase string budget) error = %v", errCamel)
		}
		if got := ojson.GetBytes(outCamel, "generation_config.thinking_level").String(); got == "" {
			t.Fatalf("expected generation_config.thinking_level to be populated from camelCase integer string budget; body=%s", outCamel)
		}
		if ojson.GetBytes(outCamel, "generationConfig").Exists() {
			t.Fatalf("expected empty generationConfig to be deleted; body=%s", outCamel)
		}

		invalidBody := []byte(`{"generation_config":{"thinking_budget":"not-a-number"},"generationConfig":{"thinking_budget":"2048"}}`)
		outInvalid, errInvalid := thinking.ApplyThinkingWithModelInfo(invalidBody, invalidBody, model.ID, "interactions", "interactions", "gemini", model)
		if errInvalid != nil {
			t.Fatalf("ApplyThinkingWithModelInfo(invalid fallback) error = %v", errInvalid)
		}
		if got := ojson.GetBytes(outInvalid, "generation_config.thinking_level").String(); got == "" {
			t.Fatalf("expected unparseable string budget to be skipped in favor of valid fallback; body=%s", outInvalid)
		}
	})
}

func TestSummaryConfig_SnakeCaseAndCamelCaseContainers(t *testing.T) {
	extractTests := []struct {
		name     string
		format   string
		body     string
		wantMode thinking.SummaryMode
	}{
		{
			name:     "antigravity_request_generation_config_thinking_config_include_thoughts",
			format:   "antigravity",
			body:     `{"request":{"generation_config":{"thinking_config":{"include_thoughts":true}}}}`,
			wantMode: thinking.SummaryEnabled,
		},
		{
			name:     "antigravity_request_generation_config_thinkingConfig_includeThoughts",
			format:   "antigravity",
			body:     `{"request":{"generation_config":{"thinkingConfig":{"includeThoughts":false}}}}`,
			wantMode: thinking.SummaryDisabled,
		},
		{
			name:     "interactions_camelCase_generationConfig_thinkingSummaries",
			format:   "interactions",
			body:     `{"generationConfig":{"thinkingSummaries":"none"}}`,
			wantMode: thinking.SummaryDisabled,
		},
		{
			name:     "interactions_camelCase_generationConfig_thinking_config_include_thoughts",
			format:   "interactions",
			body:     `{"generationConfig":{"thinking_config":{"include_thoughts":true}}}`,
			wantMode: thinking.SummaryEnabled,
		},
	}

	for _, tc := range extractTests {
		t.Run("extract/"+tc.name, func(t *testing.T) {
			got := thinking.ExtractSummaryConfig([]byte(tc.body), tc.format)
			if got.Mode != tc.wantMode {
				t.Fatalf("ExtractSummaryConfig() = %+v, want mode=%v", got, tc.wantMode)
			}
		})
	}

	applyTests := []struct {
		name       string
		format     string
		body       string
		canonical  string
		wantAbsent []string
	}{
		{
			name:      "gemini_cleans_generationConfig_thinking_config_and_generation_config_thinkingConfig",
			format:    "gemini",
			body:      `{"generationConfig":{"thinking_config":{"include_thoughts":true}},"generation_config":{"thinkingConfig":{"includeThoughts":false}}}`,
			canonical: "generationConfig.thinkingConfig.includeThoughts",
			wantAbsent: []string{
				"generationConfig.thinking_config.include_thoughts",
				"generationConfig.thinking_config",
				"generation_config.thinkingConfig.includeThoughts",
				"generation_config.thinkingConfig",
				"generation_config",
			},
		},
		{
			name:      "antigravity_cleans_request_generation_config_aliases",
			format:    "antigravity",
			body:      `{"request":{"generation_config":{"thinking_config":{"include_thoughts":true}}}}`,
			canonical: "request.generationConfig.thinkingConfig.includeThoughts",
			wantAbsent: []string{
				"request.generation_config.thinking_config.include_thoughts",
				"request.generation_config.thinking_config",
				"request.generation_config",
			},
		},
		{
			name:      "interactions_cleans_generationConfig_summary_aliases",
			format:    "interactions",
			body:      `{"generationConfig":{"thinkingSummaries":"auto","thinking_config":{"include_thoughts":true}}}`,
			canonical: "generation_config.thinking_summaries",
			wantAbsent: []string{
				"generationConfig.thinkingSummaries",
				"generationConfig.thinking_config.include_thoughts",
				"generationConfig.thinking_config",
				"generationConfig",
			},
		},
	}

	for _, tc := range applyTests {
		t.Run("apply/"+tc.name, func(t *testing.T) {
			out := thinking.ApplySummaryConfig([]byte(tc.body), tc.format, thinking.SummaryConfig{Mode: thinking.SummaryEnabled})
			if !ojson.GetBytes(out, tc.canonical).Exists() {
				t.Fatalf("missing canonical path %s in body=%s", tc.canonical, out)
			}
			for _, stale := range tc.wantAbsent {
				if got := ojson.GetBytes(out, stale); got.Exists() {
					t.Fatalf("expected stale path %s to be deleted, got %s in body=%s", stale, got.Raw, out)
				}
			}
		})
	}
}
