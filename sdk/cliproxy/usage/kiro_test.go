package usage

import "testing"

func TestKiroCanonicalUsageKeepsCacheInsideInput(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{InputTokens: 35, OutputTokens: 7, TotalTokens: 42, CacheReadTokens: 20, CacheCreationTokens: 5}, "kiro", "KiroExecutor")
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality != TokenAccountingQualityComplete || breakdown.Input.UncachedTokens != 10 || breakdown.Input.CacheReadTokens != 20 || breakdown.Input.CacheWriteTokens != 5 || breakdown.TotalTokens != 42 {
		t.Fatalf("Kiro usage was double-counted or left unclassified: %+v", breakdown)
	}
}

func TestQoderCanonicalUsageKeepsCacheInsideInput(t *testing.T) {
	detail := EnsureTokenBreakdownForProvider(Detail{InputTokens: 35, OutputTokens: 7, TotalTokens: 42, CacheReadTokens: 20}, "qoder-cn", "QoderExecutor")
	if breakdown := detail.TokenBreakdown; !breakdown.Valid() || breakdown.Quality != TokenAccountingQualityComplete || breakdown.Input.UncachedTokens != 15 {
		t.Fatalf("Qoder usage was left unclassified: %+v", breakdown)
	}
}
