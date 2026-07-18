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
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking", true},
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

func TestEnsureClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"already has claude prefix", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"contains claude mid-string is reversed", "my-claude-custom", "claude-fable-5-dd-motsuc-edualc-ym"},
		{"uppercase Claude prefix is reversed", "Claude-Opus-4", "claude-fable-5-dd-4-supO-edualC"},
		{"gpt model is reversed", "gpt-4o", "claude-fable-5-dd-o4-tpg"},
		{"gemini model is reversed", "gemini-2.5-pro", "claude-fable-5-dd-orp-5.2-inimeg"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnsureClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("EnsureClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestResolveClaudeModelIDPrefix(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{"empty", "", ""},
		{"plain claude id unchanged", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"non encoded id unchanged", "gpt-4o", "gpt-4o"},
		{"encoded gpt model", "claude-fable-5-dd-o4-tpg", "gpt-4o"},
		{"encoded gemini model", "claude-fable-5-dd-orp-5.2-inimeg", "gemini-2.5-pro"},
		{"empty encoded body unchanged", "claude-fable-5-dd-", "claude-fable-5-dd-"},
		{"preserves thinking suffix", "claude-fable-5-dd-o4-tpg(high)", "gpt-4o(high)"},
		{"round trip", EnsureClaudeModelIDPrefix("custom-model-x"), "custom-model-x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveClaudeModelIDPrefix(tt.id); got != tt.want {
				t.Fatalf("ResolveClaudeModelIDPrefix(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
