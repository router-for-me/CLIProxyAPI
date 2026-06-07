package helps

import "testing"

func TestHasClaudeToolUseOrResultMarkers(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "no markers", body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), want: false},
		{name: "tool use", body: []byte(`{"content":[{"type":"tool_use"}]}`), want: true},
		{name: "tool result", body: []byte(`{"content":[{"type":"tool_result"}]}`), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasClaudeToolUseOrResultMarkers(tt.body); got != tt.want {
				t.Fatalf("HasClaudeToolUseOrResultMarkers() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasClaudeToolResultMarker(t *testing.T) {
	if HasClaudeToolResultMarker([]byte(`{"content":[{"type":"text"}]}`)) {
		t.Fatal("expected false without tool_result")
	}
	if !HasClaudeToolResultMarker([]byte(`{"content":[{"type":"tool_result"}]}`)) {
		t.Fatal("expected true with tool_result")
	}
}
