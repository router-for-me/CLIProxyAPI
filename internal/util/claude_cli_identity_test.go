package util

import "testing"

func TestStripLeadingCLIIdentityPrefix(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "agent sdk prefix with body",
			text: "You are a Claude agent, built on Anthropic's Claude Agent SDK.\n\nYou are an interactive agent.",
			want: "You are an interactive agent.",
		},
		{
			name: "claude code prefix with body",
			text: "You are Claude Code, Anthropic's official CLI for Claude.\nKeep the rest.",
			want: "Keep the rest.",
		},
		{
			name: "sdk preset prefix with body",
			text: "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. Rest",
			want: "Rest",
		},
		{
			name: "identity only collapses to empty",
			text: "You are a Claude agent, built on Anthropic's Claude Agent SDK.",
			want: "",
		},
		{
			name: "other system text unchanged",
			text: "You are a helpful assistant.",
			want: "You are a helpful assistant.",
		},
		{
			name: "identity mid-text is kept",
			text: "Intro\nYou are Claude Code, Anthropic's official CLI for Claude.",
			want: "Intro\nYou are Claude Code, Anthropic's official CLI for Claude.",
		},
		{
			name: "empty text",
			text: "",
			want: "",
		},
	}
	for _, tt := range tests {
		if got := StripLeadingCLIIdentityPrefix(tt.text); got != tt.want {
			t.Errorf("StripLeadingCLIIdentityPrefix(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}
