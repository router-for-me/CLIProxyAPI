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
