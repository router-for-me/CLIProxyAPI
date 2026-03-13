package config

import "testing"

func TestSanitizeClaudeKeys_SystemPromptCountBounds(t *testing.T) {
	cfg := &Config{
		ClaudeKey: []ClaudeKey{
			{SystemPromptCount: -5},
			{SystemPromptCount: MaxClaudeSystemPromptCount + 50},
		},
	}

	cfg.SanitizeClaudeKeys()

	if got := cfg.ClaudeKey[0].SystemPromptCount; got != 0 {
		t.Fatalf("claude[0].SystemPromptCount = %d, want 0", got)
	}
	if got := cfg.ClaudeKey[1].SystemPromptCount; got != MaxClaudeSystemPromptCount {
		t.Fatalf("claude[1].SystemPromptCount = %d, want %d", got, MaxClaudeSystemPromptCount)
	}
}
