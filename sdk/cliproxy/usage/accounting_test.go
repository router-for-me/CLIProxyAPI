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

func TestEnsureTokenBreakdownForProviderPreservesEmptyDetail(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{}, "xai", "")
	if detail != (Detail{}) {
		t.Fatalf("detail = %+v, want empty usage detail", detail)
	}
}

func TestEnsureTokenBreakdownForProviderRenormalizesNonEmptyInvalidBreakdown(t *testing.T) {
	legacy := TokenBreakdown{
		SchemaVersion: 1,
		Quality:       TokenAccountingQualityComplete,
	}
	detail := EnsureTokenBreakdownForProvider(Detail{TokenBreakdown: legacy}, "openai", "")
	if !detail.TokenBreakdown.Valid() {
		t.Fatalf("token breakdown remained invalid: %+v", detail.TokenBreakdown)
	}
	if detail.TokenBreakdown.SchemaVersion != TokenAccountingSchemaVersion {
		t.Fatalf("schema version = %d, want %d", detail.TokenBreakdown.SchemaVersion, TokenAccountingSchemaVersion)
	}
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted for zero-token legacy breakdown", detail.CacheInputMode)
	}
}

func TestEnsureTokenBreakdownForProviderHonorsExplicitCacheInputMode(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		detail    Detail
		wantMode  CacheInputMode
		wantTotal int64
		wantInput int64
	}{
		{
			name:     "explicit separate overrides OpenAI provider heuristic",
			provider: "openai",
			detail: Detail{
				InputTokens:     100,
				OutputTokens:    10,
				CacheReadTokens: 40,
				CacheInputMode:  CacheInputModeSeparate,
			},
			wantMode:  CacheInputModeSeparate,
			wantTotal: 150,
			wantInput: 140,
		},
		{
			name:     "explicit included overrides Anthropic provider heuristic",
			provider: "anthropic",
			detail: Detail{
				InputTokens:     100,
				OutputTokens:    10,
				CacheReadTokens: 40,
				CacheInputMode:  CacheInputModeIncluded,
			},
			wantMode:  CacheInputModeIncluded,
			wantTotal: 110,
			wantInput: 100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := EnsureTokenBreakdownForProvider(tt.detail, tt.provider, "")
			if detail.CacheInputMode != tt.wantMode {
				t.Fatalf("cache input mode = %q, want %q", detail.CacheInputMode, tt.wantMode)
			}
			if detail.TotalTokens != tt.wantTotal || detail.TokenBreakdown.TotalTokens != tt.wantTotal {
				t.Fatalf("totals = detail:%d breakdown:%d, want %d", detail.TotalTokens, detail.TokenBreakdown.TotalTokens, tt.wantTotal)
			}
			if detail.TokenBreakdown.Input.TotalTokens != tt.wantInput {
				t.Fatalf("input total = %d, want %d", detail.TokenBreakdown.Input.TotalTokens, tt.wantInput)
			}
		})
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
		wantMode     CacheInputMode
	}{
		{
			name:       "OpenAI subsets cache and reasoning",
			provider:   "openai",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  130,
			wantInput:  100,
			wantOutput: 30,
			wantMode:   CacheInputModeIncluded,
		},
		{
			name:         "OpenAI compatible executor takes precedence",
			provider:     "anthropic",
			executorType: "OpenAICompatExecutor",
			detail:       Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:    130,
			wantInput:    100,
			wantOutput:   30,
			wantMode:     CacheInputModeIncluded,
		},
		{
			name:       "Gemini keeps reasoning separate",
			provider:   "gemini",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  142,
			wantInput:  100,
			wantOutput: 42,
			wantMode:   CacheInputModeIncluded,
		},
		{
			name:       "Claude keeps cache and reasoning independent",
			provider:   "anthropic",
			detail:     Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12, CacheReadTokens: 40, CacheCreationTokens: 10},
			wantTotal:  192,
			wantInput:  150,
			wantOutput: 42,
			wantMode:   CacheInputModeSeparate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := EnsureTokenBreakdownForProvider(tt.detail, tt.provider, tt.executorType)
			if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != TokenAccountingQualityComplete {
				t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
			}
			if detail.CacheInputMode != tt.wantMode {
				t.Fatalf("cache input mode = %q, want %q", detail.CacheInputMode, tt.wantMode)
			}
			if detail.TotalTokens != tt.wantTotal || detail.TokenBreakdown.TotalTokens != tt.wantTotal ||
				detail.TokenBreakdown.Input.TotalTokens != tt.wantInput || detail.TokenBreakdown.Output.TotalTokens != tt.wantOutput {
				t.Fatalf("detail = %+v, want total=%d input=%d output=%d", detail, tt.wantTotal, tt.wantInput, tt.wantOutput)
			}
		})
	}
}

func TestEnsureTokenBreakdownForIndependentProviderPromotesLegacyCachedTokens(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:  100,
		OutputTokens: 30,
		CachedTokens: 40,
	}, "anthropic", "")

	if detail.CacheInputMode != CacheInputModeSeparate {
		t.Fatalf("cache input mode = %q, want %q", detail.CacheInputMode, CacheInputModeSeparate)
	}
	if detail.CacheReadTokens != 40 {
		t.Fatalf("cache read tokens = %d, want promoted legacy value 40", detail.CacheReadTokens)
	}
	if detail.TotalTokens != 170 || detail.TokenBreakdown.TotalTokens != 170 {
		t.Fatalf("totals = detail:%d breakdown:%d, want 170", detail.TotalTokens, detail.TokenBreakdown.TotalTokens)
	}
	if detail.TokenBreakdown.Quality != TokenAccountingQualityComplete ||
		detail.TokenBreakdown.Input.TotalTokens != 140 || detail.TokenBreakdown.Input.CacheReadTokens != 40 {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}
}

func TestEnsureTokenBreakdownForUnknownProviderDoesNotGuessReasoning(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{InputTokens: 100, OutputTokens: 30, ReasoningTokens: 12}, "plugin-provider", "")
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted for unknown provider", detail.CacheInputMode)
	}
	if detail.TotalTokens != 130 || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified || detail.TokenBreakdown.UnclassifiedTokens != 130 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownForUnknownProviderHonorsExplicitSeparateCacheMode(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     100,
		OutputTokens:    30,
		ReasoningTokens: 12,
		CacheReadTokens: 40,
		CacheInputMode:  CacheInputModeSeparate,
	}, "plugin-provider", "")

	if detail.CacheInputMode != CacheInputModeSeparate {
		t.Fatalf("cache input mode = %q, want %q", detail.CacheInputMode, CacheInputModeSeparate)
	}
	if detail.TotalTokens != 170 || detail.TokenBreakdown.TotalTokens != 170 {
		t.Fatalf("totals = detail:%d breakdown:%d, want 170", detail.TotalTokens, detail.TokenBreakdown.TotalTokens)
	}
	if detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified || detail.TokenBreakdown.UnclassifiedTokens != 170 {
		t.Fatalf("token breakdown = %+v, want reasoning overlap left unclassified", detail.TokenBreakdown)
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
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted when cache exceeds input", detail.CacheInputMode)
	}
	if detail.TotalTokens != 13 || detail.CacheReadTokens != 13 || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified ||
		detail.TokenBreakdown.UnclassifiedTokens != 13 {
		t.Fatalf("detail = %+v", detail)
	}
}

func TestEnsureTokenBreakdownOmitsIncludedModeWhenLegacyCacheExceedsInput(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{InputTokens: 10, CachedTokens: 20}, "openai", "")
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted when legacy cache exceeds input", detail.CacheInputMode)
	}
}

func TestEnsureTokenBreakdownOmitsIncludedModeWhenMixedLegacyCacheExceedsInput(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     10,
		CachedTokens:    20,
		CacheReadTokens: 2,
	}, "openai", "")
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted when legacy cache exceeds input", detail.CacheInputMode)
	}
}

func TestEnsureTokenBreakdownDoesNotInferModeOverExistingCanonicalBreakdown(t *testing.T) {
	detail := Detail{
		InputTokens:         10,
		OutputTokens:        6,
		CacheReadTokens:     4,
		CacheCreationTokens: 2,
		ReasoningTokens:     1,
		TotalTokens:         16,
		TokenBreakdown:      NewSubsetTokenBreakdown(10, 4, 2, 6, 1, 16),
	}
	detail = EnsureTokenBreakdownForProvider(detail, "anthropic", "")
	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want omitted rather than contradicting existing breakdown", detail.CacheInputMode)
	}
	if detail.TokenBreakdown.Input.UncachedTokens != 4 {
		t.Fatalf("token breakdown changed: %+v", detail.TokenBreakdown)
	}
}

func TestEnsureTokenBreakdownClearsConflictingExplicitModeOverCanonicalBreakdown(t *testing.T) {
	canonical := NewSubsetTokenBreakdown(100, 40, 0, 10, 0, 110)
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 40,
		TotalTokens:     110,
		CacheInputMode:  CacheInputModeSeparate,
		TokenBreakdown:  canonical,
	}, "openai", "")

	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want conflicting mode removed", detail.CacheInputMode)
	}
	if detail.TokenBreakdown != canonical || detail.TotalTokens != 110 {
		t.Fatalf("canonical breakdown changed: detail=%+v", detail)
	}
}

func TestEnsureTokenBreakdownClearsModeWhenDetailCacheCountersDisagreeWithCanonical(t *testing.T) {
	canonical := NewSubsetTokenBreakdown(100, 0, 0, 10, 0, 110)
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 40,
		TotalTokens:     110,
		CacheInputMode:  CacheInputModeSeparate,
		TokenBreakdown:  canonical,
	}, "openai", "")

	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want removed when detail and canonical cache buckets disagree", detail.CacheInputMode)
	}
	if detail.TokenBreakdown != canonical || detail.TotalTokens != 110 {
		t.Fatalf("canonical breakdown changed: detail=%+v", detail)
	}
}

func TestEnsureTokenBreakdownClearsImpossibleExplicitModeAfterDerivation(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     10,
		CacheReadTokens: 20,
		CacheInputMode:  CacheInputModeIncluded,
	}, "openai", "")

	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want impossible included mode removed", detail.CacheInputMode)
	}
	if detail.TokenBreakdown.Quality != TokenAccountingQualityInconsistent {
		t.Fatalf("token breakdown quality = %q, want inconsistent; detail=%+v", detail.TokenBreakdown.Quality, detail)
	}
}

func TestEnsureTokenBreakdownClearsImpossibleIncludedModeOverUnclassifiedBreakdown(t *testing.T) {
	canonical := NewUnclassifiedTokenBreakdown(20)
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     10,
		CacheReadTokens: 20,
		TotalTokens:     20,
		CacheInputMode:  CacheInputModeIncluded,
		TokenBreakdown:  canonical,
	}, "openai", "")

	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want impossible included mode removed", detail.CacheInputMode)
	}
	if detail.TokenBreakdown != canonical || detail.TotalTokens != 20 {
		t.Fatalf("canonical breakdown changed: detail=%+v", detail)
	}
}

func TestEnsureTokenBreakdownPreservesIncludedModeForPartialUnclassifiedTotal(t *testing.T) {
	canonical := NewPartialSubsetTokenBreakdown(10, 0, 0, 0, 0, 15)
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:    10,
		TotalTokens:    15,
		CacheInputMode: CacheInputModeIncluded,
		TokenBreakdown: canonical,
	}, "openai", "")

	if detail.CacheInputMode != CacheInputModeIncluded {
		t.Fatalf("cache input mode = %q, want %q for valid partial total", detail.CacheInputMode, CacheInputModeIncluded)
	}
	if detail.TokenBreakdown != canonical || detail.TotalTokens != 15 {
		t.Fatalf("partial canonical breakdown changed: detail=%+v", detail)
	}
}

func TestEnsureTokenBreakdownClearsSeparateModeOverPartialIncludedBreakdown(t *testing.T) {
	canonical := NewPartialSubsetTokenBreakdown(10, 2, 0, 0, 0, 15)
	detail := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     10,
		CacheReadTokens: 2,
		TotalTokens:     15,
		CacheInputMode:  CacheInputModeSeparate,
		TokenBreakdown:  canonical,
	}, "openai", "")

	if detail.CacheInputMode != "" {
		t.Fatalf("cache input mode = %q, want conflicting separate mode removed", detail.CacheInputMode)
	}
	if detail.TokenBreakdown != canonical || detail.TotalTokens != 15 {
		t.Fatalf("partial canonical breakdown changed: detail=%+v", detail)
	}
}

func TestEnsureTokenBreakdownMixedLegacyAndCanonicalCacheDisagreementIsModeFree(t *testing.T) {
	first := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:     10,
		CachedTokens:    3,
		CacheReadTokens: 2,
	}, "openai", "")
	second := EnsureTokenBreakdownForProvider(first, "openai", "")

	for name, detail := range map[string]Detail{"first": first, "second": second} {
		if detail.CacheInputMode != "" {
			t.Fatalf("%s cache input mode = %q, want omitted for disagreeing cache counters", name, detail.CacheInputMode)
		}
		if detail.TokenBreakdown.Input.CacheReadTokens != 2 || detail.TokenBreakdown.Input.UncachedTokens != 8 {
			t.Fatalf("%s canonical input breakdown = %+v", name, detail.TokenBreakdown.Input)
		}
	}
}

func TestEnsureTokenBreakdownLegacyIncludedCacheIsIdempotent(t *testing.T) {
	first := EnsureTokenBreakdownForProvider(Detail{
		InputTokens:  10,
		CachedTokens: 4,
	}, "openai", "")
	second := EnsureTokenBreakdownForProvider(first, "openai", "")

	for name, detail := range map[string]Detail{"first": first, "second": second} {
		if detail.CacheInputMode != CacheInputModeIncluded {
			t.Fatalf("%s cache input mode = %q, want %q", name, detail.CacheInputMode, CacheInputModeIncluded)
		}
		if detail.CacheReadTokens != 4 || detail.TokenBreakdown.Input.CacheReadTokens != 4 {
			t.Fatalf("%s cache read tokens = detail:%d breakdown:%d, want 4", name, detail.CacheReadTokens, detail.TokenBreakdown.Input.CacheReadTokens)
		}
		if detail.TokenBreakdown.Input.UncachedTokens != 6 || detail.TotalTokens != 10 {
			t.Fatalf("%s detail = %+v", name, detail)
		}
	}
	if second.TokenBreakdown != first.TokenBreakdown {
		t.Fatalf("second normalization changed canonical breakdown:\nfirst=%+v\nsecond=%+v", first.TokenBreakdown, second.TokenBreakdown)
	}
}

func TestEnsureTokenBreakdownDoesNotOverrideCanonicalZeroCacheRead(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{CachedTokens: 13, CacheCreationTokens: 13}, "openai", "")
	if detail.CacheReadTokens != 0 {
		t.Fatalf("detail = %+v", detail)
	}
}
