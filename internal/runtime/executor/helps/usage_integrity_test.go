package helps

import (
	"testing"
)

func TestUsageIntegrityPreservesClaudeCacheTTL(t *testing.T) {
	d := ParseClaudeUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":40,"ephemeral_1h_input_tokens":60}}}`))
	if !d.UsageObserved || string(d.RawUsage) == "" || d.CacheCreation5mTokens != 40 || d.CacheCreation1hTokens != 60 {
		t.Fatalf("lost TTL/measurement provenance: %+v", d)
	}
}
func TestUsageIntegrityPreservesProviderCostWithoutTokens(t *testing.T) {
	d := ParseOpenAIUsage([]byte(`{"usage":{"cost_in_usd_ticks":123456789}}`))
	if !d.UsageObserved || d.CostUSD == nil || *d.CostUSD != "0.0123456789" {
		t.Fatalf("lost provider cost: %+v", d)
	}
}
func TestUsageIntegrityMissingAndMeasuredZeroDiffer(t *testing.T) {
	if ParseOpenAIUsage([]byte(`{}`)).UsageObserved {
		t.Fatal("missing usage was marked measured")
	}
	if !ParseOpenAIUsage([]byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0}}`)).UsageObserved {
		t.Fatal("measured zero lost")
	}
}
func TestUsageIntegrityDeepSeekLegacyCache(t *testing.T) {
	d := ParseOpenAIUsage([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_cache_hit_tokens":80}}`))
	if d.CacheReadTokens != 80 {
		t.Fatalf("legacy cache lost: %+v", d)
	}
}
