package common

import "testing"

func TestClaudeStopReasonToOpenAIFinishReason(t *testing.T) {
	tests := []struct{ in, want string }{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"content_filter", "stop"},    // no direct equivalent
		{"unknown_reason", "stop"},    // fallback
		{"", "stop"},                  // empty fallback
	}
	for _, tc := range tests {
		got := ClaudeStopReasonToOpenAIFinishReason(tc.in)
		if got != tc.want {
			t.Errorf("ClaudeStopReasonToOpenAIFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOpenAIFinishReasonToClaudeStopReason(t *testing.T) {
	tests := []struct {
		in         string
		hasToolCall bool
		want       string
	}{
		{"stop", false, "end_turn"},
		{"stop", true, "tool_use"},
		{"length", false, "max_tokens"},
		{"tool_calls", false, "tool_use"},
		{"content_filter", false, "refusal"},
		{"content_filter", true, "refusal"},
		{"function_call", false, "tool_use"}, // Legacy
		{"unknown", false, "end_turn"},
		{"", false, "end_turn"},
	}
	for _, tc := range tests {
		got := OpenAIFinishReasonToClaudeStopReason(tc.in, tc.hasToolCall)
		if got != tc.want {
			t.Errorf("OpenAIFinishReasonToClaudeStopReason(%q, %v) = %q, want %q",
				tc.in, tc.hasToolCall, got, tc.want)
		}
	}
}

func TestOpenAIFinishReasonToGeminiFinishReason(t *testing.T) {
	tests := []struct{ in, want string }{
		{"stop", "STOP"},
		{"length", "MAX_TOKENS"},
		{"tool_calls", "STOP"},
		{"content_filter", "SAFETY"},
		{"unknown", "STOP"},
		{"", "STOP"},
	}
	for _, tc := range tests {
		got := OpenAIFinishReasonToGeminiFinishReason(tc.in)
		if got != tc.want {
			t.Errorf("OpenAIFinishReasonToGeminiFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClaudeStopReasonToGeminiFinishReason(t *testing.T) {
	tests := []struct{ in, want string }{
		{"end_turn", "STOP"},
		{"tool_use", "STOP"},
		{"max_tokens", "MAX_TOKENS"},
		{"stop_sequence", "STOP"},
		{"content_filter", "STOP"}, // fallback
		{"unknown", "STOP"},
		{"", "STOP"},
	}
	for _, tc := range tests {
		got := ClaudeStopReasonToGeminiFinishReason(tc.in)
		if got != tc.want {
			t.Errorf("ClaudeStopReasonToGeminiFinishReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCodexStopReasonToClaudeStopReason(t *testing.T) {
	tests := []struct {
		in          string
		hasToolCall bool
		want        string
	}{
		{"stop", false, "end_turn"},
		{"completed", false, "end_turn"},
		{"", false, "end_turn"},
		{"max_tokens", false, "max_tokens"},
		{"max_output_tokens", false, "max_tokens"},
		{"tool_use", false, "end_turn"}, // Codex tool_use -> end_turn when no tool call emitted
		{"content_filter", false, "refusal"},
		{"end_turn", false, "end_turn"},
		{"stop_sequence", false, "stop_sequence"},
		{"refusal", false, "refusal"},
		{"stop", true, "tool_use"}, // hasToolCall wins
		{"max_tokens", true, "tool_use"}, // hasToolCall wins
		{"unknown", false, "end_turn"},
	}
	for _, tc := range tests {
		got := CodexStopReasonToClaudeStopReason(tc.in, tc.hasToolCall)
		if got != tc.want {
			t.Errorf("CodexStopReasonToClaudeStopReason(%q, %v) = %q, want %q",
				tc.in, tc.hasToolCall, got, tc.want)
		}
	}
}
