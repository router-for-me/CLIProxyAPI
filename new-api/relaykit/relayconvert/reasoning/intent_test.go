package reasoning

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeExplicitAndSuffix(t *testing.T) {
	t.Parallel()

	budget1024 := 1024
	budget2048 := 2048
	budget4096 := 4096

	tests := []struct {
		name         string
		explicit     Intent
		suffix       Intent
		wantMode     Mode
		wantEffort   Effort
		wantBudget   *int
		wantNoBudget bool
		wantThoughts *bool
		wantCodes    []string
	}{
		{
			name:       "enabled plus matching effort merges",
			explicit:   Intent{Mode: ModeEnabled, Effort: EffortHigh},
			suffix:     Intent{Mode: ModeEnabled, Effort: EffortHigh, Source: SourceSuffix},
			wantMode:   ModeEnabled,
			wantEffort: EffortHigh,
		},
		{
			name:       "suffix disable wins over explicit enable",
			explicit:   Intent{Mode: ModeEnabled, Effort: EffortHigh},
			suffix:     Intent{Mode: ModeDisabled, Effort: EffortNone, Source: SourceSuffix},
			wantMode:   ModeDisabled,
			wantEffort: EffortNone,
			wantCodes:  []string{"suffix_overrode_request"},
		},
		{
			name:       "bare suffix enable wins over explicit disable",
			explicit:   Intent{Mode: ModeDisabled, Effort: EffortNone},
			suffix:     Intent{Mode: ModeEnabled, Source: SourceSuffix},
			wantMode:   ModeEnabled,
			wantEffort: "",
			wantCodes:  []string{"suffix_overrode_request"},
		},
		{
			name:       "suffix effort wins over explicit effort",
			explicit:   Intent{Mode: ModeEnabled, Effort: EffortLow},
			suffix:     Intent{Mode: ModeEnabled, Effort: EffortHigh, Source: SourceSuffix},
			wantMode:   ModeEnabled,
			wantEffort: EffortHigh,
			wantCodes:  []string{"suffix_overrode_request"},
		},
		{
			name:       "suffix budget wins over explicit budget",
			explicit:   Intent{BudgetTokens: &budget1024},
			suffix:     Intent{BudgetTokens: &budget2048, Source: SourceSuffix, BudgetSource: SourceSuffix},
			wantMode:   ModeEnabled,
			wantBudget: &budget2048,
			wantCodes:  []string{"suffix_overrode_request"},
		},
		{
			name:       "suffix budget drops explicit effort",
			explicit:   Intent{Mode: ModeEnabled, Effort: EffortHigh},
			suffix:     Intent{BudgetTokens: &budget1024, Source: SourceSuffix, BudgetSource: SourceSuffix},
			wantMode:   ModeEnabled,
			wantEffort: "",
			wantBudget: &budget1024,
			wantCodes:  []string{"suffix_overrode_request"},
		},
		{
			name:         "suffix effort drops explicit budget",
			explicit:     Intent{BudgetTokens: &budget4096},
			suffix:       Intent{Mode: ModeEnabled, Effort: EffortHigh, Source: SourceSuffix},
			wantMode:     ModeEnabled,
			wantEffort:   EffortHigh,
			wantNoBudget: true,
			wantCodes:    []string{"suffix_overrode_request"},
		},
		{
			name:       "bare suffix enable keeps explicit strength",
			explicit:   Intent{Mode: ModeEnabled, Effort: EffortLow, BudgetTokens: &budget4096},
			suffix:     Intent{Mode: ModeEnabled, Source: SourceSuffix},
			wantMode:   ModeEnabled,
			wantEffort: EffortLow,
			wantBudget: &budget4096,
		},
		{
			name:         "suffix only is adopted",
			suffix:       Intent{Mode: ModeEnabled, Effort: EffortMedium, Source: SourceSuffix},
			wantMode:     ModeEnabled,
			wantEffort:   EffortMedium,
			wantThoughts: nil,
		},
		{
			name:         "explicit include thoughts overlays empty suffix strength",
			explicit:     Intent{IncludeThoughts: boolPtr(false)},
			suffix:       Intent{Mode: ModeEnabled, Effort: EffortLow, Source: SourceSuffix},
			wantMode:     ModeEnabled,
			wantEffort:   EffortLow,
			wantThoughts: boolPtr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, diagnostics, err := MergeExplicitAndSuffix(tt.explicit, tt.suffix, "claude-opus-4-8")
			require.NoError(t, err)
			assert.Equal(t, tt.wantMode, got.Mode)
			assert.Equal(t, tt.wantEffort, got.Effort)
			if tt.wantBudget != nil {
				require.NotNil(t, got.BudgetTokens)
				assert.Equal(t, *tt.wantBudget, *got.BudgetTokens)
			}
			if tt.wantNoBudget {
				assert.Nil(t, got.BudgetTokens)
			}
			if tt.wantThoughts != nil {
				require.NotNil(t, got.IncludeThoughts)
				assert.Equal(t, *tt.wantThoughts, *got.IncludeThoughts)
			}
			assert.Equal(t, tt.wantCodes, diagnosticCodes(diagnostics))
		})
	}
}

func TestFromOpenAIChatResolvesFieldConflictsWithDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("reasoning_effort wins over nested effort", func(t *testing.T) {
		t.Parallel()
		got, diagnostics, err := FromOpenAIChat(&dto.GeneralOpenAIRequest{
			ReasoningEffort: "low",
			Reasoning:       []byte(`{"effort":"high"}`),
		})
		require.NoError(t, err)
		assert.Equal(t, ModeEnabled, got.Mode)
		assert.Equal(t, EffortLow, got.Effort)
		assert.Equal(t, []string{"explicit_fields_conflict"}, diagnosticCodes(diagnostics))
	})

	t.Run("zero nested budget disables despite enabled flag", func(t *testing.T) {
		t.Parallel()
		got, diagnostics, err := FromOpenAIChat(&dto.GeneralOpenAIRequest{
			Reasoning: []byte(`{"enabled":true,"max_tokens":0}`),
		})
		require.NoError(t, err)
		assert.Equal(t, ModeDisabled, got.Mode)
		assert.Equal(t, EffortNone, got.Effort)
		assert.Equal(t, []string{"explicit_fields_conflict"}, diagnosticCodes(diagnostics))
	})

	t.Run("unknown effort value is still rejected", func(t *testing.T) {
		t.Parallel()
		_, _, err := FromOpenAIChat(&dto.GeneralOpenAIRequest{ReasoningEffort: "ultra"})
		require.ErrorIs(t, err, ErrUnsupportedEffort)
	})
}

func TestFromClaudeKeepsOutOfRangeBudgetsForTheRenderer(t *testing.T) {
	t.Parallel()

	maxTokens := uint(4096)
	for _, budget := range []int{512, 4096} {
		got, diagnostics, err := FromClaude(&dto.ClaudeRequest{
			MaxTokens: &maxTokens,
			Thinking:  &dto.Thinking{Type: "enabled", BudgetTokens: &budget},
		})
		require.NoError(t, err)
		assert.Equal(t, ModeEnabled, got.Mode)
		require.NotNil(t, got.BudgetTokens)
		assert.Equal(t, budget, *got.BudgetTokens)
		assert.Empty(t, diagnostics)
	}

	got, diagnostics, err := FromClaude(&dto.ClaudeRequest{Thinking: &dto.Thinking{Type: "auto"}})
	require.NoError(t, err)
	assert.Equal(t, ModeEnabled, got.Mode)
	assert.Equal(t, []string{"claude_thinking_type_coerced"}, diagnosticCodes(diagnostics))
}

func TestIntentStateRoundTrip(t *testing.T) {
	t.Parallel()

	budget := 4096
	include := true
	intent := Intent{
		Mode:            ModeEnabled,
		Effort:          EffortHigh,
		BudgetTokens:    &budget,
		IncludeThoughts: &include,
	}

	state := StateFromIntent(intent)
	require.NotNil(t, state)
	got := IntentFromState(state)
	assert.Equal(t, intent.Mode, got.Mode)
	assert.Equal(t, intent.Effort, got.Effort)
	require.NotNil(t, got.BudgetTokens)
	assert.Equal(t, budget, *got.BudgetTokens)
	require.NotNil(t, got.IncludeThoughts)
	assert.True(t, *got.IncludeThoughts)
	assert.True(t, IntentFromState(nil).IsEmpty())
	assert.Nil(t, StateFromIntent(Intent{}))
}

func boolPtr(v bool) *bool {
	return &v
}

func diagnosticCodes(diagnostics []types.ConversionDiagnostic) []string {
	var codes []string
	for _, diagnostic := range diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

func TestOpenAIPivotRetainsExactStrengthAndBudget(t *testing.T) {
	budget, include := 16384, false
	for _, effort := range []Effort{EffortMax, EffortXHigh} {
		t.Run(string(effort), func(t *testing.T) {
			intent := Intent{Mode: ModeEnabled, Effort: effort, BudgetTokens: &budget, IncludeThoughts: &include}
			chat := &dto.GeneralOpenAIRequest{}
			require.NoError(t, ApplyToOpenAIChat(chat, intent))
			assert.Equal(t, string(effort), chat.ReasoningEffort)
			restored, _, err := FromOpenAIChat(chat)
			require.NoError(t, err)
			assert.Equal(t, effort, restored.Effort)
			require.NotNil(t, restored.BudgetTokens)
			assert.Equal(t, budget, *restored.BudgetTokens)
			require.NotNil(t, restored.IncludeThoughts)
			assert.False(t, *restored.IncludeThoughts)

			responses := &dto.OpenAIResponsesRequest{}
			require.NoError(t, ApplyToOpenAIResponses(responses, restored))
			require.NotNil(t, responses.Reasoning)
			assert.Equal(t, string(effort), responses.Reasoning.Effort)
			restored, _, err = FromOpenAIResponses(responses)
			require.NoError(t, err)
			assert.Equal(t, effort, restored.Effort)
			require.NotNil(t, restored.BudgetTokens)
			assert.Equal(t, budget, *restored.BudgetTokens)
			require.NotNil(t, restored.IncludeThoughts)
			assert.False(t, *restored.IncludeThoughts)
		})
	}
}

func TestOpenAIPivotExplicitEffortOverridesPivotWithDiagnostic(t *testing.T) {
	intent := Intent{Mode: ModeEnabled, Effort: EffortMax}
	chat := &dto.GeneralOpenAIRequest{}
	require.NoError(t, ApplyToOpenAIChat(chat, intent))
	chat.ReasoningEffort = "xhigh"
	got, diagnostics, err := FromOpenAIChat(chat)
	require.NoError(t, err)
	assert.Equal(t, EffortXHigh, got.Effort)
	assert.Equal(t, []string{"explicit_fields_conflict"}, diagnosticCodes(diagnostics))

	responses := &dto.OpenAIResponsesRequest{}
	require.NoError(t, ApplyToOpenAIResponses(responses, intent))
	responses.Reasoning.Effort = "xhigh"
	got, diagnostics, err = FromOpenAIResponses(responses)
	require.NoError(t, err)
	assert.Equal(t, EffortXHigh, got.Effort)
	assert.Equal(t, []string{"explicit_fields_conflict"}, diagnosticCodes(diagnostics))
}
