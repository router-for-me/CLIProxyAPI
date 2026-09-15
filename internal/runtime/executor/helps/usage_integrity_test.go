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

func TestGeminiFamilyStreamPreservesMeasuredZero(t *testing.T) {
	tests := []struct {
		name     string
		protocol string
		payload  string
	}{
		{"gemini", "gemini", `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`},
		{"gemini snake case", "gemini", `data: {"usage_metadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`},
		{"interactions", "interactions", `data: {"type":"interaction.completed","interaction":{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`},
		{"interactions Gemini fields", "interactions", `data: {"type":"interaction.completed","interaction":{"usage":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}}`},
		{"antigravity", "antigravity", `data: {"response":{"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buffer StreamUsageBuffer
			ObservePluginExecutorStreamUsage(tt.protocol, []byte(tt.payload), &buffer)
			detail, ok := buffer.Detail()
			if !ok || !detail.UsageObserved || detail.RawUsage == "" || detail.TotalTokens != 0 {
				t.Fatalf("measured zero was lost: detail=%+v ok=%v", detail, ok)
			}
			var missing StreamUsageBuffer
			ObservePluginExecutorStreamUsage(tt.protocol, []byte(`data: {"candidates":[{"finishReason":"STOP"}]}`), &missing)
			if detail, ok := missing.Detail(); ok || detail.UsageObserved {
				t.Fatalf("missing usage became measured: detail=%+v ok=%v", detail, ok)
			}
		})
	}
}

func TestStreamUsageBufferPreservesMeasuredZeroWithTier(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"default","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`))
	buffer.ObserveOpenAIStream([]byte(`data: {"service_tier":"priority","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	detail, ok := buffer.Detail()
	if !ok || !detail.UsageObserved || detail.RawUsage == "" || detail.TotalTokens != 0 || detail.ResponseServiceTier != "priority" {
		t.Fatalf("final measured zero with tier was lost: detail=%+v ok=%v", detail, ok)
	}
	var zeroOnly StreamUsageBuffer
	zeroOnly.ObserveOpenAIStream([]byte(`data: {"service_tier":"priority","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	detail, ok = zeroOnly.Detail()
	if !ok || !detail.UsageObserved || detail.RawUsage == "" || detail.TotalTokens != 0 {
		t.Fatalf("only measured zero with tier was lost: detail=%+v ok=%v", detail, ok)
	}
}
