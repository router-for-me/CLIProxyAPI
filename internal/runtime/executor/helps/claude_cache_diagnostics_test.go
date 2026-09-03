package helps

import "testing"

const nonStreamCacheBody = `{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5",` +
	`"usage":{"input_tokens":2,"cache_creation_input_tokens":35314,"cache_read_input_tokens":161937,` +
	`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":35314},"output_tokens":7},` +
	`"diagnostics":{"cache_miss_reason":{"type":"messages_changed","cache_missed_input_tokens":25202}}}`

const messageStartLine = `data: {"type":"message_start","message":{"model":"claude-fable-5-1","id":"msg_011",` +
	`"type":"message","role":"assistant","content":[],"usage":{"input_tokens":2,` +
	`"cache_creation_input_tokens":197015,"cache_read_input_tokens":0,` +
	`"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":197015},"output_tokens":4},` +
	`"diagnostics":{"cache_miss_reason":{"type":"tools_changed","cache_missed_input_tokens":90151}}}}`

const messageDeltaLine = `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},` +
	`"usage":{"input_tokens":2,"cache_creation_input_tokens":197015,"cache_read_input_tokens":0,"output_tokens":4}}`

func TestParseClaudeUsageSplitsCacheCreationAndMissReason(t *testing.T) {
	detail := ParseClaudeUsage([]byte(nonStreamCacheBody))

	if detail.CacheReadTokens != 161937 {
		t.Errorf("CacheReadTokens = %d, want 161937", detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 35314 {
		t.Errorf("CacheCreationTokens = %d, want 35314", detail.CacheCreationTokens)
	}
	if detail.CacheCreation5mTokens != 0 {
		t.Errorf("CacheCreation5mTokens = %d, want 0", detail.CacheCreation5mTokens)
	}
	if detail.CacheCreation1hTokens != 35314 {
		t.Errorf("CacheCreation1hTokens = %d, want 35314", detail.CacheCreation1hTokens)
	}
	if detail.CacheMissReason != "messages_changed" {
		t.Errorf("CacheMissReason = %q, want messages_changed", detail.CacheMissReason)
	}
	if detail.CacheMissedTokens != 25202 {
		t.Errorf("CacheMissedTokens = %d, want 25202", detail.CacheMissedTokens)
	}
	if !detail.TokenBreakdown.Valid() {
		t.Errorf("TokenBreakdown is not valid: %+v", detail.TokenBreakdown)
	}
}

func TestParseClaudeCacheAnnotationFromMessageStart(t *testing.T) {
	annotation, ok := ParseClaudeCacheAnnotation([]byte(messageStartLine))
	if !ok {
		t.Fatal("ParseClaudeCacheAnnotation(message_start) returned false")
	}
	if annotation.CacheCreation1hTokens != 197015 {
		t.Errorf("CacheCreation1hTokens = %d, want 197015", annotation.CacheCreation1hTokens)
	}
	if annotation.CacheCreation5mTokens != 0 {
		t.Errorf("CacheCreation5mTokens = %d, want 0", annotation.CacheCreation5mTokens)
	}
	if annotation.CacheMissReason != "tools_changed" {
		t.Errorf("CacheMissReason = %q, want tools_changed", annotation.CacheMissReason)
	}
	if annotation.CacheMissedTokens != 90151 {
		t.Errorf("CacheMissedTokens = %d, want 90151", annotation.CacheMissedTokens)
	}
}

func TestParseClaudeCacheAnnotationIgnoresOtherEvents(t *testing.T) {
	if _, ok := ParseClaudeCacheAnnotation([]byte(messageDeltaLine)); ok {
		t.Error("message_delta must not produce a cache annotation")
	}
	if _, ok := ParseClaudeCacheAnnotation([]byte("event: ping")); ok {
		t.Error("a non-JSON SSE line must not produce a cache annotation")
	}
}

func TestClaudeCacheAnnotationApplyFillsOnlyMissingFields(t *testing.T) {
	annotation, ok := ParseClaudeCacheAnnotation([]byte(messageStartLine))
	if !ok {
		t.Fatal("ParseClaudeCacheAnnotation(message_start) returned false")
	}

	// message_delta carries the authoritative totals but drops the pool split
	// and the diagnostics object; the annotation supplies both.
	detail, ok := ParseClaudeStreamUsage([]byte(messageDeltaLine))
	if !ok {
		t.Fatal("ParseClaudeStreamUsage(message_delta) returned false")
	}
	merged := annotation.Apply(detail)

	if merged.OutputTokens != 4 {
		t.Errorf("OutputTokens = %d, want the message_delta value 4", merged.OutputTokens)
	}
	if merged.CacheCreation1hTokens != 197015 {
		t.Errorf("CacheCreation1hTokens = %d, want 197015", merged.CacheCreation1hTokens)
	}
	if merged.CacheMissReason != "tools_changed" {
		t.Errorf("CacheMissReason = %q, want tools_changed", merged.CacheMissReason)
	}
	if merged.CacheMissedTokens != 90151 {
		t.Errorf("CacheMissedTokens = %d, want 90151", merged.CacheMissedTokens)
	}

	// An already-populated split must win over the annotation.
	detail.CacheCreation5mTokens = 11
	detail.CacheCreation1hTokens = 22
	detail.CacheMissReason = "already_set"
	kept := annotation.Apply(detail)
	if kept.CacheCreation5mTokens != 11 || kept.CacheCreation1hTokens != 22 {
		t.Errorf("Apply overwrote an existing split: %d/%d", kept.CacheCreation5mTokens, kept.CacheCreation1hTokens)
	}
	if kept.CacheMissReason != "already_set" {
		t.Errorf("Apply overwrote an existing miss reason: %q", kept.CacheMissReason)
	}
}

func TestParseClaudeStreamUsageKeepsMessageDeltaAsUsageSource(t *testing.T) {
	if _, ok := ParseClaudeStreamUsage([]byte(messageStartLine)); ok {
		t.Error("message_start must not be treated as the usage source; message_delta is authoritative")
	}
}
