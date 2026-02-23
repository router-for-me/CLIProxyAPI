package executor

import (
	"testing"

	"github.com/tidwall/gjson"
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

func TestParseOpenAIStreamUsageResponsesParity(t *testing.T) {
	line := []byte(`data: {"usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":5}}}`)
	detail, ok := parseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage to be parsed")
	}
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 13 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 13)
	}
	if detail.TotalTokens != 24 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 24)
	}
	if detail.CachedTokens != 3 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 3)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsage_WithAlternateFieldsAndStringValues(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":"10","output_tokens":"20","prompt_tokens":"11","completion_tokens":"12","prompt_tokens_details":{"cached_token_count":"7"},"output_tokens_details":{"reasoning_token_count":"9"}}}`)
	detail := parseOpenAIUsage(data)
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 12 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 12)
	}
	if detail.TotalTokens != 23 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 23)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIStreamUsage_WithAlternateFieldsAndStringValues(t *testing.T) {
	line := []byte(`data: {"usage":{"prompt_tokens":"3","completion_tokens":"4","prompt_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":"2"}}}`)
	detail, ok := parseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("expected stream usage")
	}
	if detail.InputTokens != 3 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 3)
	}
	if detail.OutputTokens != 4 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 4)
	}
	if detail.TotalTokens != 7 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 7)
	}
	if detail.CachedTokens != 1 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 1)
	}
	if detail.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 2)
	}
}

func TestParseOpenAIResponsesUsageDetail_WithAlternateFields(t *testing.T) {
	node := gjson.Parse(`{"input_tokens":"14","completion_tokens":"16","cached_tokens":"1","output_tokens_details":{"reasoning_tokens":"3"}}`)
	detail := parseOpenAIResponsesUsageDetail(node)
	if detail.InputTokens != 14 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 14)
	}
	if detail.OutputTokens != 16 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 16)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 1 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 1)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 3)
	}
}

func TestParseOpenAIStreamUsage_EmptyAndMalformedUsagePayloads(t *testing.T) {
	detail, ok := parseOpenAIStreamUsage([]byte(`data: {"usage":{}}`))
	if !ok {
		t.Fatal("expected empty usage object to parse")
	}
	if detail.TotalTokens != 0 || detail.InputTokens != 0 || detail.OutputTokens != 0 {
		t.Fatalf("expected zero usage detail for empty payload, got %+v", detail)
	}

	if _, ok := parseOpenAIStreamUsage([]byte(`data: {"usage":`)); ok {
		t.Fatal("expected malformed usage payload to be rejected")
	}
	if _, ok := parseOpenAIStreamUsage([]byte(`data: {"id":"x"}`)); ok {
		t.Fatal("expected payload without usage to be rejected")
	}
}
