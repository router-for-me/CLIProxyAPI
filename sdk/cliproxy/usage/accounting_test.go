package usage

import "testing"

func TestNewSubsetTokenBreakdownAvoidsCacheAndReasoningDoubleCount(t *testing.T) {
	breakdown := NewSubsetTokenBreakdown(100, 40, 10, 30, 12, 130)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Input.UncachedTokens != 50 || breakdown.Output.NonReasoningTokens != 18 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
	if breakdown.TotalTokens != 130 {
		t.Fatalf("total = %d, want 130", breakdown.TotalTokens)
	}
}

func TestNewPartialSubsetTokenBreakdownPreservesKnownBuckets(t *testing.T) {
	breakdown := NewPartialSubsetTokenBreakdown(10, 4, 0, 0, 0, 15)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Quality != TokenAccountingQualityUnclassified || breakdown.Input.TotalTokens != 10 ||
		breakdown.UnclassifiedTokens != 5 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestNewIndependentTokenBreakdownKeepsClaudeCacheBucketsIndependent(t *testing.T) {
	breakdown := NewIndependentTokenBreakdown(30, 7, 13, 5, 0, 55)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Input.TotalTokens != 50 || breakdown.TotalTokens != 55 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestNewSeparateReasoningTokenBreakdownAddsReasoningToOutput(t *testing.T) {
	breakdown := NewSeparateReasoningTokenBreakdown(20, 5, 0, 7, 3, 30)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Output.TotalTokens != 10 || breakdown.TotalTokens != 30 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestTokenBreakdownMarksContradictoryParentsInconsistent(t *testing.T) {
	breakdown := NewSubsetTokenBreakdown(10, 4, 0, 3, 1, 20)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Quality != TokenAccountingQualityInconsistent || breakdown.UnclassifiedTokens != 20 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestNewUnclassifiedTokenBreakdownDoesNotGuessBuckets(t *testing.T) {
	breakdown := NewUnclassifiedTokenBreakdown(42)
	if !breakdown.Valid() {
		t.Fatalf("breakdown is invalid: %+v", breakdown)
	}
	if breakdown.Quality != TokenAccountingQualityUnclassified || breakdown.UnclassifiedTokens != 42 {
		t.Fatalf("breakdown = %+v", breakdown)
	}
}

func TestEnsureTokenBreakdownForProviderUsesKnownSemantics(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		executorType string
		detail       Detail
		wantTotal    int64
		wantInput    int64
		wantOutput   int64
	}{
		{
			name:       "OpenAI subsets cache and reasoning",
			provider:   "openai",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  130,
			wantInput:  100,
			wantOutput: 30,
		},
		{
			name:         "OpenAI compatible executor takes precedence",
			provider:     "anthropic",
			executorType: "OpenAICompatExecutor",
			detail:       Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:    130,
			wantInput:    100,
			wantOutput:   30,
		},
		{
			name:       "Gemini keeps reasoning separate",
			provider:   "gemini",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  142,
			wantInput:  100,
			wantOutput: 42,
		},
		{
			name:       "Claude keeps cache and reasoning independent",
			provider:   "anthropic",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  192,
			wantInput:  150,
			wantOutput: 42,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := EnsureTokenBreakdownForProvider(tt.detail, tt.provider, tt.executorType)
			if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != TokenAccountingQualityComplete {
				t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
			}
			if detail.TotalTokens != tt.wantTotal || detail.TokenBreakdown.TotalTokens != tt.wantTotal ||
				detail.TokenBreakdown.Input.TotalTokens != tt.wantInput || detail.TokenBreakdown.Output.TotalTokens != tt.wantOutput {
				t.Fatalf("detail = %+v, want total=%d input=%d output=%d", detail, tt.wantTotal, tt.wantInput, tt.wantOutput)
			}
		})
	}
}

func TestEnsureTokenBreakdownForUnknownProviderDoesNotGuessReasoning(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12}, "plugin-provider", "")
	if detail.TotalTokens != 130 || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified || detail.TokenBreakdown.UnclassifiedTokens != 130 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownForUnknownProviderPreservesAuxiliaryOnlyUsage(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{ReasoningTokens: 12, CacheReadTokens: 7}, "plugin-provider", "")
	if detail.TotalTokens != 19 || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified || detail.TokenBreakdown.UnclassifiedTokens != 19 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownForGeminiClassifiesReasoningOnlyUsage(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{ReasoningTokens: 12}, "gemini", "")
	if detail.TotalTokens != 12 || detail.TokenBreakdown.Quality != TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Output.ReasoningTokens != 12 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownPreservesLegacyCachedOnlyUsage(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{CachedTokens: 13}, "openai", "")
	if detail.TotalTokens != 13 || detail.CacheReadTokens != 13 || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified ||
		detail.TokenBreakdown.UnclassifiedTokens != 13 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownDoesNotOverrideCanonicalZeroCacheRead(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{CachedTokens: 13, CacheCreationTokens: 13}, "openai", "")
	if detail.CacheReadTokens != 0 {
		t.Fatalf("detail = %+v", detail)
	}
}
