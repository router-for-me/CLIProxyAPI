package executor

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := parseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := parseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &usageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

	func TestClaudeStreamAccumulator_FullSequence(t *testing.T) {
	// Simulate: message_start (input+cache) → thinking_delta → message_delta (output)
	var acc claudeStreamUsageAccumulator
	acc.processLine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":50000}}}`))
	acc.processLine([]byte(`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Let me think about this carefully step by step"}}`))
	acc.processLine([]byte(`data: {"type":"message_delta","usage":{"output_tokens":200}}`))

	if !acc.sawUsage {
		t.Fatal("sawUsage should be true after processing usage events")
	}
	if acc.detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want 10", acc.detail.InputTokens)
	}
	if acc.detail.OutputTokens != 200 {
		t.Fatalf("output tokens = %d, want 200", acc.detail.OutputTokens)
	}
	if acc.detail.CachedTokens != 50000 {
		t.Fatalf("cached tokens = %d, want 50000", acc.detail.CachedTokens)
	}
	expectedThinking := int64(len("Let me think about this carefully step by step")) / claudeThinkingTokenFactor
	if acc.thinkingLen/claudeThinkingTokenFactor != expectedThinking {
		t.Fatalf("thinking tokens = %d, want %d", acc.thinkingLen/claudeThinkingTokenFactor, expectedThinking)
	}
}

	func TestClaudeStreamAccumulator_MessageStartOnly(t *testing.T) {
	var acc claudeStreamUsageAccumulator
	acc.processLine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":100000}}}`))

	if !acc.sawUsage {
		t.Fatal("sawUsage should be true after message_start with usage")
	}
	if acc.detail.InputTokens != 5 {
		t.Fatalf("input tokens = %d, want 5", acc.detail.InputTokens)
	}
	if acc.detail.OutputTokens != 0 {
		t.Fatalf("output tokens = %d, want 0", acc.detail.OutputTokens)
	}
	if acc.detail.CachedTokens != 100000 {
		t.Fatalf("cached tokens = %d, want 100000", acc.detail.CachedTokens)
	}
}

	func TestClaudeStreamAccumulator_MessageDeltaOnly(t *testing.T) {
	var acc claudeStreamUsageAccumulator
	acc.processLine([]byte(`data: {"type":"message_delta","usage":{"output_tokens":150}}`))

	if !acc.sawUsage {
		t.Fatal("sawUsage should be true after message_delta with usage")
	}
	if acc.detail.InputTokens != 0 {
		t.Fatalf("input tokens = %d, want 0", acc.detail.InputTokens)
	}
	if acc.detail.OutputTokens != 150 {
		t.Fatalf("output tokens = %d, want 150", acc.detail.OutputTokens)
	}
}

	func TestClaudeStreamAccumulator_ThinkingDeltaOnly_NoUsage(t *testing.T) {
	// Stream with only thinking deltas but no usage event — should not publish
	var acc claudeStreamUsageAccumulator
	acc.processLine([]byte(`data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"some thinking text"}}`))

	if acc.sawUsage {
		t.Fatal("sawUsage should be false when no usage event was received")
	}
	if acc.thinkingLen == 0 {
		t.Fatal("thinkingLen should be non-zero after processing thinking_delta")
	}
}

	func TestClaudeStreamAccumulator_NoEvents(t *testing.T) {
	// Empty/interrupted stream — should not publish
	var acc claudeStreamUsageAccumulator
	if acc.sawUsage {
		t.Fatal("sawUsage should be false for empty accumulator")
	}
}

	func TestClaudeStreamAccumulator_CacheCreationFallback(t *testing.T) {
	var acc claudeStreamUsageAccumulator
	acc.processLine([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"cache_creation_input_tokens":80000}}}`))

	if acc.detail.CachedTokens != 80000 {
		t.Fatalf("cached tokens = %d, want 80000 (from cache_creation fallback)", acc.detail.CachedTokens)
	}
}
