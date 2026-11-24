package clawmaster

import (
	"reflect"
	"testing"
)

// Tokens billed above the standard rate are split by rate: a fast-mode request's tokens are all
// "fast" (its hour-long writes "fast_1h"); otherwise only hour-long cache writes count, as "1h".
func TestPremiumTokensSplitByRate(t *testing.T) {
	counts := tokenCounts{input: 1, output: 2, cacheRead: 3, cacheCreation: 30, cacheCreation1h: 20}
	if got, want := premiumTokens(counts, false), []premiumCount{{"cache_creation", "1h", 20}}; !reflect.DeepEqual(got, want) {
		t.Errorf("standard: %v, want %v", got, want)
	}
	want := []premiumCount{{"input", "fast", 1}, {"output", "fast", 2}, {"cache_read", "fast", 3}, {"cache_creation", "fast", 10}, {"cache_creation", "fast_1h", 20}}
	if got := premiumTokens(counts, true); !reflect.DeepEqual(got, want) {
		t.Errorf("fast: %v, want %v", got, want)
	}
	// More hour-long writes than writes (a malformed usage) are clamped; none are none.
	if got, want := premiumTokens(tokenCounts{cacheCreation: 5, cacheCreation1h: 9}, false), []premiumCount{{"cache_creation", "1h", 5}}; !reflect.DeepEqual(got, want) {
		t.Errorf("clamped: %v, want %v", got, want)
	}
	if got := premiumTokens(tokenCounts{input: 5, cacheCreation: 5}, false); len(got) != 0 {
		t.Errorf("standard request with 5-minute writes: %v, want none", got)
	}
}

// The usage object splits cache writes by duration; the scanner keeps the hour-long part, in a
// JSON body and in a stream whose later message_delta carries only the flat counts.
func TestUsageScannerReadsOneHourCacheWrites(t *testing.T) {
	body := `{"usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}`
	got, ok := scanUsage(200, "application/json", body)
	if !ok || got.cacheCreation != 30 || got.cacheCreation1h != 20 {
		t.Fatalf("JSON counts %+v, %v", got, ok)
	}
	stream := `data: {"type":"message_start","message":{"usage":{"input_tokens":1,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20},"output_tokens":1}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"input_tokens":1,"cache_creation_input_tokens":30,"output_tokens":9}}` + "\n\n"
	got, ok = scanUsage(200, "text/event-stream", stream)
	if !ok || got.cacheCreation != 30 || got.cacheCreation1h != 20 || got.output != 9 {
		t.Fatalf("stream counts %+v, %v", got, ok)
	}
}
