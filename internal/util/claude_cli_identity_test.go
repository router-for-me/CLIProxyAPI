package util

import "testing"

func TestStripLeadingCLIIdentityPrefix(t *testing.T) {
	const identity = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	tests := []struct {
		name string
		text string
		want string
	}{
		{"body with whitespace", identity + " \t\r\n\n  Body\n", " \t\r\n\n  Body\n"},
		{"body without separator", identity + "Body", "Body"},
		{"identity only", identity, ""},
		{"whitespace only suffix", identity + " \t\r\n", " \t\r\n"},
		{"default identity unchanged", "You are Claude Code, Anthropic's official CLI for Claude.\nBody", "You are Claude Code, Anthropic's official CLI for Claude.\nBody"},
		{"preset identity unchanged", "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. Body", "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. Body"},
		{"other text unchanged", "You are a helpful assistant.", "You are a helpful assistant."},
		{"mid-text unchanged", "Intro\n" + identity, "Intro\n" + identity},
		{"leading whitespace unchanged", "\n" + identity, "\n" + identity},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripLeadingCLIIdentityPrefix(tt.text); got != tt.want {
				t.Errorf("StripLeadingCLIIdentityPrefix(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
