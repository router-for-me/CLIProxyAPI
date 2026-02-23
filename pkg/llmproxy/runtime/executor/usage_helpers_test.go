package executor

import (
	"testing"
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

func TestParseOpenAIStreamUsageSSE(t *testing.T) {
	line := []byte(`data: {"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail, ok := parseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected usage to be parsed")
	}
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 22 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 22)
	}
	if detail.TotalTokens != 33 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 33)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIStreamUsageNoUsage(t *testing.T) {
	line := []byte(`data: {"choices":[{"delta":{"content":"ping"}}]}`)
	_, ok := parseOpenAIStreamUsage(line)
	if ok {
		t.Fatal("expected usage parse to fail when usage is absent")
	}
}

func TestParseOpenAIResponsesStreamUsageSSE(t *testing.T) {
	line := []byte(`data: {"usage":{"input_tokens":7,"output_tokens":9,"total_tokens":16,"input_tokens_details":{"cached_tokens":2},"output_tokens_details":{"reasoning_tokens":3}}}`)
	detail, ok := parseOpenAIResponsesStreamUsage(line)
	if !ok {
		t.Fatal("expected responses stream usage to be parsed")
	}
	if detail.InputTokens != 7 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 7)
	}
	if detail.OutputTokens != 9 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 9)
	}
	if detail.TotalTokens != 16 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 16)
	}
	if detail.CachedTokens != 2 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 2)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 3)
	}
}

func TestParseOpenAIResponsesUsageTotalFallback(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":4,"output_tokens":6}}`)
	detail := parseOpenAIResponsesUsage(data)
	if detail.TotalTokens != 10 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 10)
	}
}

func TestParseOpenAIUsage_PrefersCompletionTokensWhenOutputTokensZero(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":12,"output_tokens":0,"completion_tokens":9}}`)
	detail := parseOpenAIUsage(data)
	if detail.OutputTokens != 9 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 9)
	}
	if detail.TotalTokens != 21 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 21)
	}
}

func TestParseOpenAIStreamUsage_PrefersCompletionTokensWhenOutputTokensZero(t *testing.T) {
	line := []byte(`data: {"usage":{"prompt_tokens":7,"output_tokens":0,"completion_tokens":5}}`)
	detail, ok := parseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage to be parsed")
	}
	if detail.OutputTokens != 5 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 5)
	}
	if detail.TotalTokens != 12 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 12)
	}
}
