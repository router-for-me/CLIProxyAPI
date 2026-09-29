package reasoning

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderGeminiMapsAndClampsInsteadOfRejecting(t *testing.T) {
	t.Parallel()

	budget8192 := 8192
	budget100000 := 100000
	budget100 := 100

	tests := []struct {
		name       string
		model      string
		intent     Intent
		wantNil    bool
		wantLevel  string
		wantBudget *int
		wantEffort Effort
		wantCodes  []string
	}{
		{
			name:       "budget on gemini 3.1 pro becomes level",
			model:      "gemini-3.1-pro-preview",
			intent:     Intent{BudgetTokens: &budget8192},
			wantLevel:  "medium",
			wantEffort: EffortMedium,
			wantCodes:  []string{"gemini_budget_to_level"},
		},
		{
			name:       "medium on gemini 3 pro moves to high",
			model:      "gemini-3-pro-preview",
			intent:     Intent{Mode: ModeEnabled, Effort: EffortMedium},
			wantLevel:  "high",
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:       "xhigh on gemini 3 flash moves to high",
			model:      "gemini-3-flash-preview",
			intent:     Intent{Mode: ModeEnabled, Effort: EffortXHigh},
			wantLevel:  "high",
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:       "effort on gemini 2.5 flash becomes budget",
			model:      "gemini-2.5-flash",
			intent:     Intent{Mode: ModeEnabled, Effort: EffortHigh},
			wantBudget: intPtr(24576),
			wantEffort: EffortHigh,
		},
		{
			name:       "oversized budget on gemini 2.5 flash is clamped",
			model:      "gemini-2.5-flash",
			intent:     Intent{BudgetTokens: &budget100000},
			wantBudget: intPtr(24576),
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_budget_clamped"},
		},
		{
			name:       "undersized budget on gemini 2.5 flash lite is raised",
			model:      "gemini-2.5-flash-lite",
			intent:     Intent{BudgetTokens: &budget100},
			wantBudget: intPtr(512),
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_budget_clamped"},
		},
		{
			name:       "nothinking on gemini 3.1 pro uses lowest level",
			model:      "gemini-3.1-pro-preview",
			intent:     Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix},
			wantLevel:  "low",
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_thinking_disable_unsupported"},
		},
		{
			name:       "nothinking on gemini 2.5 pro uses minimum budget",
			model:      "gemini-2.5-pro",
			intent:     Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix},
			wantBudget: intPtr(128),
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_thinking_disable_unsupported"},
		},
		{
			name:       "nothinking on gemini 2.5 flash disables",
			model:      "gemini-2.5-flash",
			intent:     Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix},
			wantBudget: intPtr(0),
			wantEffort: EffortNone,
		},
		{
			name:      "unknown model gets no synthesized config",
			model:     "gemini-2.0-flash-thinking-exp",
			intent:    Intent{Mode: ModeEnabled, Source: SourceSuffix},
			wantNil:   true,
			wantCodes: []string{"gemini_unknown_capability"},
		},
		{
			name:      "image model drops thinking controls",
			model:     "gemini-2.5-flash-image",
			intent:    Intent{Mode: ModeEnabled, Source: SourceSuffix},
			wantNil:   true,
			wantCodes: []string{"gemini_thinking_unsupported"},
		},
		{
			name:       "gemini 3 pro image keeps include thoughts only",
			model:      "gemini-3-pro-image-preview",
			intent:     Intent{Mode: ModeEnabled, Effort: EffortLow, IncludeThoughts: boolPtr(true)},
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_thinking_unsupported"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderGemini(tt.model, tt.intent, nil, 0)
			require.NoError(t, err)
			assert.Equal(t, tt.wantEffort, got.EffectiveEffort)
			assert.Equal(t, tt.wantCodes, diagnosticCodes(got.Diagnostics))
			if tt.wantNil {
				assert.Nil(t, got.Config)
				return
			}
			require.NotNil(t, got.Config)
			assert.Equal(t, tt.wantLevel, got.Config.ThinkingLevel)
			if tt.wantBudget == nil {
				assert.Nil(t, got.Config.ThinkingBudget)
				return
			}
			require.NotNil(t, got.Config.ThinkingBudget)
			assert.Equal(t, *tt.wantBudget, *got.Config.ThinkingBudget)
		})
	}
}

func TestNormalizeGeminiThinkingConfigRewritesNativeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		model        string
		config       dto.GeminiThinkingConfig
		wantErr      error
		wantNil      bool
		wantLevel    string
		wantBudget   *int
		wantThoughts *bool
		wantEffort   Effort
		wantCodes    []string
	}{
		{
			name:       "budget on gemini 3.1 pro becomes level",
			model:      "gemini-3.1-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(8192)},
			wantLevel:  "medium",
			wantEffort: EffortMedium,
			wantCodes:  []string{"gemini_budget_to_level"},
		},
		{
			name:       "dynamic budget on gemini 3 becomes high",
			model:      "gemini-3-flash-preview",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(-1)},
			wantLevel:  "high",
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_budget_to_level"},
		},
		{
			name:       "level on gemini 2.5 flash becomes budget",
			model:      "gemini-2.5-flash",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "high"},
			wantBudget: intPtr(24576),
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_level_to_budget"},
		},
		{
			name:       "medium on gemini 3 pro moves to high",
			model:      "gemini-3-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "medium"},
			wantLevel:  "high",
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:       "minimal on gemini 3.1 pro moves to low",
			model:      "gemini-3.1-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "minimal"},
			wantLevel:  "low",
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:       "supported level is kept without diagnostics",
			model:      "gemini-3.1-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "low"},
			wantLevel:  "low",
			wantEffort: EffortLow,
		},
		{
			name:       "uppercase enum level is canonicalized without diagnostics",
			model:      "gemini-3.7-flash",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "MEDIUM"},
			wantLevel:  "medium",
			wantEffort: EffortMedium,
		},
		{
			name:       "mixed case level with whitespace is canonicalized without diagnostics",
			model:      "gemini-3.7-flash",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: " Medium "},
			wantLevel:  "medium",
			wantEffort: EffortMedium,
		},
		{
			name:       "uppercase minimal on gemini 3 pro moves to low",
			model:      "gemini-3-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "MINIMAL"},
			wantLevel:  "low",
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:       "xhigh on gemini 3 moves to high",
			model:      "gemini-3.7-flash",
			config:     dto.GeminiThinkingConfig{ThinkingLevel: "xhigh"},
			wantLevel:  "high",
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_level_adjusted"},
		},
		{
			name:    "unknown level is rejected",
			model:   "gemini-3.7-flash",
			config:  dto.GeminiThinkingConfig{ThinkingLevel: "ULTRA"},
			wantErr: ErrUnsupportedEffort,
		},
		{
			name:       "oversized budget on gemini 2.5 flash is clamped",
			model:      "gemini-2.5-flash",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(100000)},
			wantBudget: intPtr(24576),
			wantEffort: EffortHigh,
			wantCodes:  []string{"gemini_budget_clamped"},
		},
		{
			name:       "undersized budget on gemini 2.5 flash lite is raised",
			model:      "gemini-2.5-flash-lite",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(100)},
			wantBudget: intPtr(512),
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_budget_clamped"},
		},
		{
			name:       "zero budget on gemini 2.5 pro uses minimum budget",
			model:      "gemini-2.5-pro",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(0)},
			wantBudget: intPtr(128),
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_thinking_disable_unsupported"},
		},
		{
			name:       "zero budget on gemini 2.5 flash stays off",
			model:      "gemini-2.5-flash",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(0)},
			wantBudget: intPtr(0),
			wantEffort: EffortNone,
		},
		{
			name:       "zero budget on gemini 3.1 pro uses lowest level",
			model:      "gemini-3.1-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(0)},
			wantLevel:  "low",
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_thinking_disable_unsupported"},
		},
		{
			name:       "dynamic budget on gemini 2.5 passes through",
			model:      "gemini-2.5-flash",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(-1)},
			wantBudget: intPtr(-1),
			wantEffort: EffortHigh,
		},
		{
			name:      "tts model drops the config",
			model:     "gemini-2.5-flash-preview-tts",
			config:    dto.GeminiThinkingConfig{ThinkingBudget: intPtr(1024)},
			wantNil:   true,
			wantCodes: []string{"gemini_thinking_unsupported"},
		},
		{
			name:         "gemini 3 pro image keeps include thoughts only",
			model:        "gemini-3-pro-image-preview",
			config:       dto.GeminiThinkingConfig{ThinkingBudget: intPtr(1024), IncludeThoughts: boolPtr(true)},
			wantThoughts: boolPtr(true),
			wantEffort:   EffortHigh,
			wantCodes:    []string{"gemini_thinking_unsupported"},
		},
		{
			name:       "unknown model passes the config through",
			model:      "gemini-2.0-flash-thinking-exp",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(4096)},
			wantBudget: intPtr(4096),
			wantEffort: EffortMedium,
		},
		{
			name:       "budget and level on gemini 3 keeps the level",
			model:      "gemini-3.1-pro-preview",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(8192), ThinkingLevel: "low"},
			wantLevel:  "low",
			wantEffort: EffortLow,
			wantCodes:  []string{"gemini_budget_level_conflict"},
		},
		{
			name:       "budget and level on gemini 2.5 keeps the budget",
			model:      "gemini-2.5-flash",
			config:     dto.GeminiThinkingConfig{ThinkingBudget: intPtr(4096), ThinkingLevel: "high"},
			wantBudget: intPtr(4096),
			wantEffort: EffortMedium,
			wantCodes:  []string{"gemini_budget_level_conflict"},
		},
		{
			name:    "budget and level on an unknown model is rejected",
			model:   "gemini-2.0-flash-thinking-exp",
			config:  dto.GeminiThinkingConfig{ThinkingBudget: intPtr(1024), ThinkingLevel: "high"},
			wantErr: ErrEffortConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := tt.config
			generation := &dto.GeminiChatGenerationConfig{ThinkingConfig: &config}
			effort, diagnostics, err := NormalizeGeminiThinkingConfig(tt.model, generation)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEffort, effort)
			assert.Equal(t, tt.wantCodes, diagnosticCodes(diagnostics))
			if tt.wantNil {
				assert.Nil(t, generation.ThinkingConfig)
				return
			}
			require.NotNil(t, generation.ThinkingConfig)
			assert.Equal(t, tt.wantLevel, generation.ThinkingConfig.ThinkingLevel)
			if tt.wantBudget == nil {
				assert.Nil(t, generation.ThinkingConfig.ThinkingBudget)
			} else {
				require.NotNil(t, generation.ThinkingConfig.ThinkingBudget)
				assert.Equal(t, *tt.wantBudget, *generation.ThinkingConfig.ThinkingBudget)
			}
			if tt.wantThoughts != nil {
				require.NotNil(t, generation.ThinkingConfig.IncludeThoughts)
				assert.Equal(t, *tt.wantThoughts, *generation.ThinkingConfig.IncludeThoughts)
			}
		})
	}
}
