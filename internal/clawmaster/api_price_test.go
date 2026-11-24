package clawmaster

import (
	"math"
	"testing"
)

func TestAPICostPricesEachTokenTypeAtItsRate(t *testing.T) {
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }
	million := int64(1_000_000)
	for _, tc := range []struct {
		name   string
		model  string
		counts tokenCounts
		fast   bool
		want   float64
	}{
		{"input", "claude-opus-5-5", tokenCounts{input: million}, false, 4},
		{"output", "claude-opus-5-5", tokenCounts{output: million}, false, 20},
		{"cache read", "claude-opus-5-5", tokenCounts{cacheRead: million}, false, 0.20},
		{"5-minute cache write", "claude-opus-5-5", tokenCounts{cacheCreation: million}, false, 5},
		{"1-hour cache write", "claude-opus-5-5", tokenCounts{cacheCreation: million, cacheCreation1h: million}, false, 8},
		{"mixed cache writes", "claude-sonnet-5-5", tokenCounts{cacheCreation: 2 * million, cacheCreation1h: million}, false, 2.50 + 4},
		{"Fable 5.2", "claude-fable-5-2", tokenCounts{input: million, output: million, cacheRead: million}, false, 8 + 40 + 0.20},
		{"fast mode", "claude-fable-5-2", tokenCounts{input: million, output: million, cacheRead: million}, true, 16 + 80 + 0.40},
		{"fast mode where there is none", "claude-sonnet-5-5", tokenCounts{input: million}, true, 2},
		{"a dated id", "claude-haiku-4-5-20251001", tokenCounts{input: million, output: million}, false, 1 + 5},
		{"a platform prefix", "us.anthropic.claude-sonnet-5", tokenCounts{cacheRead: million}, false, 0.20},
		// Haiku 5.5's prompt length counts cache reads and writes; at 100,000 it is still the short rate.
		{"Haiku 5.5 short prompt", "claude-haiku-5-5", tokenCounts{input: 60_000, cacheRead: 40_000, output: million}, false, 0.006 + 0.0004 + 0.50},
		{"Haiku 5.5 long prompt", "claude-haiku-5-5", tokenCounts{input: 60_000, cacheRead: 40_001, output: million}, false, 0.03 + 40_001*0.05/1e6 + 2.50},
	} {
		got, ok := apiCost(tc.model, tc.counts, tc.fast)
		if !ok || !near(got, tc.want) {
			t.Errorf("%s: apiCost = %v, %v; want %v", tc.name, got, ok, tc.want)
		}
	}
	for _, model := range []string{"claude-test-model", "other", "unknown", ""} {
		if got, ok := apiCost(model, tokenCounts{input: million}, false); ok {
			t.Errorf("%q is priced at %v", model, got)
		}
	}
}

// The usage object splits cache writes by duration; the scanner keeps the hour-long part.
func TestUsageScannerReadsOneHourCacheWrites(t *testing.T) {
	body := `{"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	got, ok := scanUsage(200, "application/json", body)
	if !ok || got.cacheCreation != 30 || got.cacheCreation1h != 20 {
		t.Fatalf("counts %+v, %v", got, ok)
	}
}
