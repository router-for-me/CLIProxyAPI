package util

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsClaudeCodeAttributionSystemText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "Claude Code attribution block",
			text: "x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=12345;",
			want: true,
		},
		{
			name: "leading whitespace",
			text: "\n\t x-anthropic-billing-header: cc_version=2.1.63.abc; cch=12345;",
			want: true,
		},
		{
			name: "regular system prompt",
			text: "You are helpful.",
			want: false,
		},
		{
			name: "empty text",
			text: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsClaudeCodeAttributionSystemText(tt.text); got != tt.want {
				t.Fatalf("IsClaudeCodeAttributionSystemText(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestStripClaudeCodeAttributionSystem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantSystem  string
		wantPresent bool
	}{
		{
			name: "string attribution deleted",
			body: `{"system":"x-anthropic-billing-header: cc_version=2.1.220; cch=abcde;","messages":[]}`,
		},
		{
			name:        "string regular prompt kept",
			body:        `{"system":"You are helpful.","messages":[]}`,
			wantSystem:  `"You are helpful."`,
			wantPresent: true,
		},
		{
			name:        "array drops billing keeps identity",
			body:        `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220; cch=abcde;"},{"type":"text","text":"You are Claude Code"}],"messages":[]}`,
			wantSystem:  `[{"type":"text","text":"You are Claude Code"}]`,
			wantPresent: true,
		},
		{
			name: "array only billing deleted",
			body: `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.220; cch=abcde;"}],"messages":[]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := StripClaudeCodeAttributionSystem([]byte(tt.body))
			system := gjson.GetBytes(got, "system")
			if system.Exists() != tt.wantPresent {
				t.Fatalf("system exists = %v, want %v: %s", system.Exists(), tt.wantPresent, got)
			}
			if tt.wantPresent && system.Raw != tt.wantSystem {
				t.Fatalf("system = %s, want %s", system.Raw, tt.wantSystem)
			}
			if strings.Contains(string(got), "cch=") {
				t.Fatalf("stripped body still contains cch=: %s", got)
			}
		})
	}
}
