package helps

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestClaudeCacheCreationDetailIterations(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    *usage.CacheCreationDetail
	}{
		{"multiple iterations", `{"usage":{"cache_creation_input_tokens":300,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}]}}`, &usage.CacheCreationDetail{Ephemeral5mInputTokens: 100, Ephemeral1hInputTokens: 200}},
		{"partial iterations", `{"usage":{"cache_creation_input_tokens":300,"iterations":[{"type":"message","cache_creation_input_tokens":100},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}]}}`, nil},
		{"mismatched total", `{"usage":{"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}}`, nil},
		{"missing bucket", `{"usage":{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_1h_input_tokens":100}}}`, nil},
		{"negative bucket", `{"usage":{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":-1,"ephemeral_1h_input_tokens":100}}}`, nil},
		{"overflow", `{"usage":{"cache_creation_input_tokens":100,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":9223372036854775807,"ephemeral_1h_input_tokens":0}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":0}}]}}`, nil},
		{"separate iteration types", `{"usage":{"cache_creation_input_tokens":300,"iterations":[{"type":"compaction","cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":0}},{"type":"advisor_message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2000}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}]}}`, &usage.CacheCreationDetail{Ephemeral5mInputTokens: 100, Ephemeral1hInputTokens: 200}},
		{"null top level", `{"usage":{"cache_creation_input_tokens":100,"cache_creation":null,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}]}}`, &usage.CacheCreationDetail{Ephemeral5mInputTokens: 100}},
		{"unknown iteration type", `{"usage":{"cache_creation_input_tokens":100,"iterations":[{"type":"future_type","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}]}}`, nil},
		{"fallback aggregation unknown", `{"usage":{"cache_creation_input_tokens":100,"iterations":[{"type":"fallback_message","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}}]}}`, nil},
		{"no message iterations", `{"usage":{"cache_creation_input_tokens":0,"iterations":[{"type":"compaction"}]}}`, nil},
		{"explicit zero", `{"usage":{"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}`, &usage.CacheCreationDetail{}},
		{"top level takes precedence", `{"usage":{"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200},"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}]}}`, &usage.CacheCreationDetail{Ephemeral5mInputTokens: 100, Ephemeral1hInputTokens: 200}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseClaudeUsage([]byte(tt.payload)).CacheCreationDetail
			if tt.want == nil {
				if got != nil {
					t.Fatalf("detail=%+v, want nil", got)
				}
			} else if got == nil || *got != *tt.want {
				t.Fatalf("detail=%+v, want %+v", got, tt.want)
			}
		})
	}
}

// Verify mixed-TTL usage across a server-side tool loop.
func TestClaudeCacheCreationToolLoopStream(t *testing.T) {
	const payload = `data: {"type":"message_start","message":{"usage":{"input_tokens":4,"cache_creation_input_tokens":2962,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2962},"output_tokens":16}}}
data: {"type":"message_delta","usage":{"input_tokens":6,"cache_creation_input_tokens":11193,"cache_read_input_tokens":2962,"output_tokens":89,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2962}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":8231,"ephemeral_1h_input_tokens":0}}]}}`
	var buffer StreamUsageBuffer
	for _, line := range strings.Split(payload, "\n") {
		buffer.ObserveClaudeStream([]byte(line))
	}
	detail, ok := buffer.Detail()
	if !ok || detail.InputTokens != 6 || detail.CacheReadTokens != 2962 || detail.CacheCreationTokens != 11193 || detail.OutputTokens != 89 || detail.TotalTokens != 14250 {
		t.Fatalf("unexpected final usage: %+v", detail)
	}
	if detail.CacheCreationDetail == nil || detail.CacheCreationDetail.Ephemeral5mInputTokens != 8231 || detail.CacheCreationDetail.Ephemeral1hInputTokens != 2962 {
		t.Fatalf("unexpected TTL split: %+v", detail.CacheCreationDetail)
	}
}

// Zero counters in a later delta are treated like the sibling input/cache-read
// counters: they do not erase usage already reported by message_start.
func TestClaudeStreamCacheCreationZeroDeltaKeepsStartUsage(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":4,"cache_creation_input_tokens":100,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":10}}`))
	detail, _ := buffer.Detail()
	if detail.CacheCreationTokens != 100 || detail.CachedTokens != 100 || detail.TotalTokens != 114 {
		t.Fatalf("zero delta must keep start usage: %+v", detail)
	}
	if detail.CacheCreationDetail == nil || detail.CacheCreationDetail.Ephemeral1hInputTokens != 100 {
		t.Fatalf("zero delta must keep start split: %+v", detail.CacheCreationDetail)
	}
}

func TestClaudeStreamCacheCreationDetailAggregateChanges(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"cache_creation_input_tokens":100,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"cache_creation_input_tokens":300,"output_tokens":10}}`))
	detail, _ := buffer.Detail()
	if detail.CacheCreationTokens != 300 || detail.CacheCreationDetail != nil {
		t.Fatalf("detail=%+v; stale split must be cleared", detail)
	}
	// Later partial updates must not resurrect a stale split.
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"output_tokens":20}}`))
	detail, _ = buffer.Detail()
	if detail.CacheCreationDetail != nil {
		t.Fatalf("stale detail resurrected: %+v", detail.CacheCreationDetail)
	}
	// A new complete split can replace the missing detail.
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"cache_creation_input_tokens":300,"iterations":[{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":0}},{"type":"message","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":200}}]}}`))
	detail, _ = buffer.Detail()
	if detail.CacheCreationDetail == nil || detail.CacheCreationDetail.Ephemeral5mInputTokens != 100 || detail.CacheCreationDetail.Ephemeral1hInputTokens != 200 {
		t.Fatalf("replacement detail=%+v", detail.CacheCreationDetail)
	}
}
