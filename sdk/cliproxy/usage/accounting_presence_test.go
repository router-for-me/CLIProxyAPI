package usage

import "testing"

func TestUnobservedUsageRemainsUnclassifiedAfterNormalization(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{TokenBreakdown: NewUnobservedTokenBreakdown()}, "openai", "plugin")
	if !detail.TokenBreakdown.Valid() || detail.TokenBreakdown.Quality != TokenAccountingQualityUnclassified || detail.TokenBreakdown.Observed == nil {
		t.Fatalf("missing measurement was normalized to measured zero: %+v", detail)
	}
	zero := WithTokenUsagePresence(NewSubsetTokenBreakdown(0, 0, 0, 0, 0, 0), TokenUsagePresence{InputTokens: true, OutputTokens: true, TotalTokens: true})
	if !zero.Valid() || zero.Quality != TokenAccountingQualityComplete {
		t.Fatalf("explicit zero must stay measured: %+v", zero)
	}
	legacy := EnsureTokenBreakdownForProvider(Detail{}, "openai", "native")
	if legacy.TokenBreakdown.Quality != TokenAccountingQualityComplete || legacy.TokenBreakdown.Observed != nil {
		t.Fatalf("legacy producer contract changed: %+v", legacy)
	}
}

func TestObservedUsageDoesNotInventUnknownSubdivisions(t *testing.T) {
	known := TokenUsagePresence{InputTokens: true, OutputTokens: true, CacheReadTokens: true, CacheWriteTokens: true, TotalTokens: true}
	breakdown := WithTokenUsagePresence(NewSubsetTokenBreakdown(100, 40, 10, 30, 0, 130), known)
	if !breakdown.Valid() || breakdown.Quality != TokenAccountingQualityUnclassified || breakdown.Input.UncachedTokens != 50 || breakdown.Output.TotalTokens != 0 || breakdown.UnclassifiedTokens != 30 {
		t.Fatalf("unknown reasoning was classified as non-reasoning: %+v", breakdown)
	}
	known.CacheWriteTokens = false
	breakdown = WithTokenUsagePresence(NewSubsetTokenBreakdown(100, 40, 0, 30, 0, 130), known)
	if !breakdown.Valid() || breakdown.Input.UncachedTokens != 0 || breakdown.Input.CacheReadTokens != 40 || breakdown.UnclassifiedTokens != 90 {
		t.Fatalf("unknown cache write was classified as uncached: %+v", breakdown)
	}
	partial := WithTokenUsagePresence(NewPartialSubsetTokenBreakdown(10, 4, 0, 0, 0, 15), TokenUsagePresence{InputTokens: true, CacheReadTokens: true, TotalTokens: true})
	if !partial.Valid() || partial.Input.UncachedTokens != 0 || partial.Input.CacheReadTokens != 4 || partial.UnclassifiedTokens != 11 {
		t.Fatalf("partial usage guessed an unknown cache subdivision: %+v", partial)
	}
}
