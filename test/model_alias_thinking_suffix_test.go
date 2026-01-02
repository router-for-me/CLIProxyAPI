package test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
)

// TestModelAliasThinkingSuffix tests the 32 test cases defined in docs/thinking_suffix_test_cases.md
// These tests verify the thinking suffix parsing and application logic across different providers.
func TestModelAliasThinkingSuffix(t *testing.T) {
	tests := []struct {
		id            int
		name          string
		provider      string
		requestModel  string
		suffixType    string
		expectedField string // "thinkingBudget", "thinkingLevel", "budget_tokens", "reasoning_effort", "enable_thinking"
		expectedValue any
		upstreamModel string // The upstream model after alias resolution
		isAlias       bool
	}{
		// === 1. Antigravity Provider ===
		// 1.1 Budget-only models (Gemini 2.5)
		{1, "antigravity_original_numeric", "antigravity", "gemini-2.5-computer-use-preview-10-2025(1000)", "numeric", "thinkingBudget", 1000, "gemini-2.5-computer-use-preview-10-2025", false},
		{2, "antigravity_alias_numeric", "antigravity", "gp(1000)", "numeric", "thinkingBudget", 1000, "gemini-2.5-computer-use-preview-10-2025", true},
		// 1.2 Budget+Levels models (Gemini 3)
		{3, "antigravity_original_numeric_to_level", "antigravity", "gemini-3-flash-preview(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{4, "antigravity_original_level", "antigravity", "gemini-3-flash-preview(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{5, "antigravity_alias_numeric_to_level", "antigravity", "gf(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", true},
		{6, "antigravity_alias_level", "antigravity", "gf(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", true},

		// === 2. Gemini CLI Provider ===
		// 2.1 Budget-only models
		{7, "gemini_cli_original_numeric", "gemini-cli", "gemini-2.5-pro(8192)", "numeric", "thinkingBudget", 8192, "gemini-2.5-pro", false},
		{8, "gemini_cli_alias_numeric", "gemini-cli", "g25p(8192)", "numeric", "thinkingBudget", 8192, "gemini-2.5-pro", true},
		// 2.2 Budget+Levels models
		{9, "gemini_cli_original_numeric_to_level", "gemini-cli", "gemini-3-flash-preview(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{10, "gemini_cli_original_level", "gemini-cli", "gemini-3-flash-preview(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{11, "gemini_cli_alias_numeric_to_level", "gemini-cli", "gf(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", true},
		{12, "gemini_cli_alias_level", "gemini-cli", "gf(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", true},

		// === 3. Vertex Provider ===
		// 3.1 Budget-only models
		{13, "vertex_original_numeric", "vertex", "gemini-2.5-pro(16384)", "numeric", "thinkingBudget", 16384, "gemini-2.5-pro", false},
		{14, "vertex_alias_numeric", "vertex", "vg25p(16384)", "numeric", "thinkingBudget", 16384, "gemini-2.5-pro", true},
		// 3.2 Budget+Levels models
		{15, "vertex_original_numeric_to_level", "vertex", "gemini-3-flash-preview(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{16, "vertex_original_level", "vertex", "gemini-3-flash-preview(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{17, "vertex_alias_numeric_to_level", "vertex", "vgf(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", true},
		{18, "vertex_alias_level", "vertex", "vgf(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", true},

		// === 4. AI Studio Provider ===
		// 4.1 Budget-only models
		{19, "aistudio_original_numeric", "aistudio", "gemini-2.5-pro(12000)", "numeric", "thinkingBudget", 12000, "gemini-2.5-pro", false},
		{20, "aistudio_alias_numeric", "aistudio", "ag25p(12000)", "numeric", "thinkingBudget", 12000, "gemini-2.5-pro", true},
		// 4.2 Budget+Levels models
		{21, "aistudio_original_numeric_to_level", "aistudio", "gemini-3-flash-preview(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{22, "aistudio_original_level", "aistudio", "gemini-3-flash-preview(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", false},
		{23, "aistudio_alias_numeric_to_level", "aistudio", "agf(1000)", "numeric", "thinkingLevel", "low", "gemini-3-flash-preview", true},
		{24, "aistudio_alias_level", "aistudio", "agf(low)", "level", "thinkingLevel", "low", "gemini-3-flash-preview", true},

		// === 5. Claude Provider ===
		{25, "claude_original_numeric", "claude", "claude-sonnet-4-5-20250929(16384)", "numeric", "budget_tokens", 16384, "claude-sonnet-4-5-20250929", false},
		{26, "claude_alias_numeric", "claude", "cs45(16384)", "numeric", "budget_tokens", 16384, "claude-sonnet-4-5-20250929", true},

		// === 6. Codex Provider ===
		{27, "codex_original_level", "codex", "gpt-5(high)", "level", "reasoning_effort", "high", "gpt-5", false},
		{28, "codex_alias_level", "codex", "g5(high)", "level", "reasoning_effort", "high", "gpt-5", true},

		// === 7. Qwen Provider ===
		{29, "qwen_original_level", "qwen", "qwen3-coder-plus(high)", "level", "enable_thinking", true, "qwen3-coder-plus", false},
		{30, "qwen_alias_level", "qwen", "qcp(high)", "level", "enable_thinking", true, "qwen3-coder-plus", true},

		// === 8. iFlow Provider ===
		{31, "iflow_original_level", "iflow", "glm-4.7(high)", "level", "reasoning_effort", "high", "glm-4.7", false},
		{32, "iflow_alias_level", "iflow", "glm(high)", "level", "reasoning_effort", "high", "glm-4.7", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Step 1: Parse model suffix (simulates SDK layer normalization)
			// For "gp(1000)" -> requestedModel="gp", metadata={thinking_budget: 1000}
			requestedModel, metadata := util.NormalizeThinkingModel(tt.requestModel)

			// Verify suffix was parsed
			if metadata == nil && (tt.suffixType == "numeric" || tt.suffixType == "level") {
				t.Errorf("Case #%d: NormalizeThinkingModel(%q) metadata is nil", tt.id, tt.requestModel)
				return
			}

			// Step 2: Simulate OAuth model mapping
			// Real flow: applyOAuthModelMapping stores requestedModel (the alias) in metadata
			if tt.isAlias {
				if metadata == nil {
					metadata = make(map[string]any)
				}
				metadata[util.ModelMappingOriginalModelMetadataKey] = requestedModel
			}

			// Step 3: Verify metadata extraction
			switch tt.suffixType {
			case "numeric":
				budget, _, _, matched := util.ThinkingFromMetadata(metadata)
				if !matched {
					t.Errorf("Case #%d: ThinkingFromMetadata did not match", tt.id)
					return
				}
				if budget == nil {
					t.Errorf("Case #%d: expected budget in metadata", tt.id)
					return
				}
				// For thinkingBudget/budget_tokens, verify the parsed budget value
				if tt.expectedField == "thinkingBudget" || tt.expectedField == "budget_tokens" {
					expectedBudget := tt.expectedValue.(int)
					if *budget != expectedBudget {
						t.Errorf("Case #%d: budget = %d, want %d", tt.id, *budget, expectedBudget)
					}
				}
				// For thinkingLevel (Gemini 3), verify conversion from budget to level
				if tt.expectedField == "thinkingLevel" {
					level, ok := util.ThinkingBudgetToGemini3Level(tt.upstreamModel, *budget)
					if !ok {
						t.Errorf("Case #%d: ThinkingBudgetToGemini3Level failed", tt.id)
						return
					}
					expectedLevel := tt.expectedValue.(string)
					if level != expectedLevel {
						t.Errorf("Case #%d: converted level = %q, want %q", tt.id, level, expectedLevel)
					}
				}

			case "level":
				_, _, effort, matched := util.ThinkingFromMetadata(metadata)
				if !matched {
					t.Errorf("Case #%d: ThinkingFromMetadata did not match", tt.id)
					return
				}
				if effort == nil {
					t.Errorf("Case #%d: expected effort in metadata", tt.id)
					return
				}
				if tt.expectedField == "thinkingLevel" || tt.expectedField == "reasoning_effort" {
					expectedEffort := tt.expectedValue.(string)
					if *effort != expectedEffort {
						t.Errorf("Case #%d: effort = %q, want %q", tt.id, *effort, expectedEffort)
					}
				}
			}

			// Step 4: Test Gemini-specific thinkingLevel conversion for Gemini 3 models
			if tt.expectedField == "thinkingLevel" && util.IsGemini3Model(tt.upstreamModel) {
				body := []byte(`{"request":{"contents":[]}}`)

				// Build metadata simulating real OAuth flow:
				// - requestedModel (alias like "gf") is stored in model_mapping_original_model
				// - upstreamModel is passed as the model parameter
				testMetadata := make(map[string]any)
				if tt.isAlias {
					// Real flow: applyOAuthModelMapping stores requestedModel (the alias)
					testMetadata[util.ModelMappingOriginalModelMetadataKey] = requestedModel
				}
				// Copy parsed metadata (thinking_budget, reasoning_effort, etc.)
				for k, v := range metadata {
					testMetadata[k] = v
				}

				result := util.ApplyGemini3ThinkingLevelFromMetadataCLI(tt.upstreamModel, testMetadata, body)
				levelVal := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel")

				expectedLevel := tt.expectedValue.(string)
				if !levelVal.Exists() {
					t.Errorf("Case #%d: expected thinkingLevel in result", tt.id)
				} else if levelVal.String() != expectedLevel {
					t.Errorf("Case #%d: thinkingLevel = %q, want %q", tt.id, levelVal.String(), expectedLevel)
				}
			}

			// Step 5: Test Gemini 2.5 thinkingBudget application using real ApplyThinkingMetadataCLI flow
			if tt.expectedField == "thinkingBudget" && util.IsGemini25Model(tt.upstreamModel) {
				body := []byte(`{"request":{"contents":[]}}`)

				// Build metadata simulating real OAuth flow:
				// - requestedModel (alias like "gp") is stored in model_mapping_original_model
				// - upstreamModel is passed as the model parameter
				testMetadata := make(map[string]any)
				if tt.isAlias {
					// Real flow: applyOAuthModelMapping stores requestedModel (the alias)
					testMetadata[util.ModelMappingOriginalModelMetadataKey] = requestedModel
				}
				// Copy parsed metadata (thinking_budget, reasoning_effort, etc.)
				for k, v := range metadata {
					testMetadata[k] = v
				}

				// Use the exported ApplyThinkingMetadataCLI which includes the fallback logic
				result := executor.ApplyThinkingMetadataCLI(body, testMetadata, tt.upstreamModel)
				budgetVal := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget")

				expectedBudget := tt.expectedValue.(int)
				if !budgetVal.Exists() {
					t.Errorf("Case #%d: expected thinkingBudget in result", tt.id)
				} else if int(budgetVal.Int()) != expectedBudget {
					t.Errorf("Case #%d: thinkingBudget = %d, want %d", tt.id, int(budgetVal.Int()), expectedBudget)
				}
			}
		})
	}
}
