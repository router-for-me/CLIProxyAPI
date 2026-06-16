package thinking

import "testing"

func TestMapToClaudeEffortPreservesNativeXHigh(t *testing.T) {
	levels := []string{"low", "medium", "high", "xhigh", "max"}
	got, ok := MapToClaudeEffort("xhigh", levels)
	if !ok || got != "xhigh" {
		t.Fatalf("MapToClaudeEffort(xhigh, opus48 levels) = (%q, %v), want (xhigh, true)", got, ok)
	}
}

func TestMapToClaudeEffortFallsBackToMaxWhenXHighMissing(t *testing.T) {
	levels := []string{"low", "medium", "high", "max"}
	got, ok := MapToClaudeEffort("xhigh", levels)
	if !ok || got != "max" {
		t.Fatalf("MapToClaudeEffort(xhigh, opus46 levels) = (%q, %v), want (max, true)", got, ok)
	}
}
