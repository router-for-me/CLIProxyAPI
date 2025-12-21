package util

import "testing"

func TestIsClaudeThinkingModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		expected bool
	}{
		// Claude thinking models - should return true
		{"claude-sonnet-4-5-thinking", "claude-sonnet-4-5-thinking", true},
		{"claude-opus-4-5-thinking", "claude-opus-4-5-thinking", true},
		{"Claude-Sonnet-Thinking uppercase", "Claude-Sonnet-4-5-Thinking", true},
		{"claude thinking mixed case", "Claude-THINKING-Model", true},

		// Non-thinking Claude models - should return false
		{"claude-sonnet-4-5 (no thinking)", "claude-sonnet-4-5", false},
		{"claude-opus-4-5 (no thinking)", "claude-opus-4-5", false},
		{"claude-3-5-sonnet", "claude-3-5-sonnet-20240620", false},

		// Non-Claude models - should return false
		{"gemini-3-pro-preview", "gemini-3-pro-preview", false},
		{"gemini-thinking model", "gemini-3-pro-thinking", false}, // not Claude
		{"gpt-4o", "gpt-4o", false},
		{"empty string", "", false},

		// Edge cases
		{"thinking without claude", "thinking-model", false},
		{"claude without thinking", "claude-model", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsClaudeThinkingModel(tt.model)
			if result != tt.expected {
				t.Errorf("IsClaudeThinkingModel(%q) = %v, expected %v", tt.model, result, tt.expected)
			}
		})
	}
}
