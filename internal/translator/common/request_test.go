package common

import "testing"

func TestRequestModelNamePrefersOriginalRequest(t *testing.T) {
	original := []byte(`{"model":"original-model"}`)
	translated := []byte(`{"model":"translated-model"}`)

	if got := RequestModelName(original, translated); got != "original-model" {
		t.Fatalf("model = %q, want original-model", got)
	}
}

func TestRequestModelNameSupportsWrappedRequest(t *testing.T) {
	request := []byte(`{"request":{"model":"wrapped-model"}}`)

	if got := RequestModelName(nil, request); got != "wrapped-model" {
		t.Fatalf("model = %q, want wrapped-model", got)
	}
}

func TestGenerateClaudeToolCallID(t *testing.T) {
	id := GenerateClaudeToolCallID()
	if len(id) != 30 {
		t.Fatalf("expected len 30 (toolu_ + 24), got %d: %q", len(id), id)
	}
	if id[:6] != "toolu_" {
		t.Fatalf("expected prefix toolu_, got %q", id)
	}
	for _, ch := range id[6:] {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')) {
			t.Fatalf("invalid character in ID %q: %c", id, ch)
		}
	}
}
