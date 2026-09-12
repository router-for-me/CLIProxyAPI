package util

import "testing"

func TestGeminiClaudeToolUseIDStableAndBound(t *testing.T) {
	args := `{"file_path":"/tmp/a","old_string":"x","new_string":"y"}`
	first := GeminiClaudeToolUseID("native-call-1", "Edit", args)
	second := GeminiClaudeToolUseID("native-call-1", "Edit", `{"new_string":"y","old_string":"x","file_path":"/tmp/a"}`)
	if first == "" || first != second || !IsGeminiClaudeToolUseID(first) {
		t.Fatalf("stable tool id mismatch: first=%q second=%q", first, second)
	}
	if changed := GeminiClaudeToolUseID("native-call-1", "Edit", `{"file_path":"/tmp/a","old_string":"x","new_string":"z"}`); changed == first {
		t.Fatal("tool id must be bound to native call semantics")
	}
	if GeminiClaudeToolUseID("", "Edit", args) != "" {
		t.Fatal("ID-less provider calls must keep the existing fallback path")
	}
	if IsGeminiClaudeToolUseID("toolu_client_value") {
		t.Fatal("ordinary client tool IDs must not be treated as CPA provenance IDs")
	}
}
