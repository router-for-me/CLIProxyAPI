package util

import "testing"

func TestToolUseNameMapFromClaudeRequest(t *testing.T) {
	raw := []byte(`{
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "toolu_1", "name": "Read_File"},
					{"type": "text", "text": "ignored"},
					{"type": "tool_use", "id": "toolu_2", "name": "Bash"},
					{"type": "tool_use", "id": "toolu_1", "name": "ignored-duplicate"}
				]
			}
		]
	}`)

	got := ToolUseNameMapFromClaudeRequest(raw)
	if len(got) != 2 {
		t.Fatalf("expected 2 tool_use mappings, got %d", len(got))
	}
	if got["toolu_1"] != "Read_File" {
		t.Fatalf("toolu_1 = %q, want %q", got["toolu_1"], "Read_File")
	}
	if got["toolu_2"] != "Bash" {
		t.Fatalf("toolu_2 = %q, want %q", got["toolu_2"], "Bash")
	}
}

func TestToolUseNameMapFromClaudeRequest_InvalidOrMissingMessages(t *testing.T) {
	tests := [][]byte{
		nil,
		[]byte(`not-json`),
		[]byte(`{"messages": {}}`),
		[]byte(`{"messages": []}`),
	}

	for _, raw := range tests {
		if got := ToolUseNameMapFromClaudeRequest(raw); got != nil {
			t.Fatalf("expected nil map for %q, got %#v", string(raw), got)
		}
	}
}
