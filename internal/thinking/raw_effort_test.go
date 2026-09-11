package thinking

import "testing"

func TestFormatRawEffortPreservesBudget(t *testing.T) {
	if got := FormatRawEffort(ThinkingConfig{Mode: ModeBudget, Budget: 31999}); got != "budget:31999" {
		t.Fatalf("FormatRawEffort() = %q, want budget:31999", got)
	}
}

func TestExtractRawReasoningEffortUsesSuffixPrecedence(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":1024}}`)
	if got := ExtractRawReasoningEffort(body, "claude", "claude-sonnet(high)"); got != "level:high" {
		t.Fatalf("ExtractRawReasoningEffort() = %q, want level:high", got)
	}
}
