package util

import "testing"

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

func TestIsSDKEntrypoint(t *testing.T) {
	tests := []struct {
		name string
		ep   string
		want bool
	}{
		{"sdk-cli", "sdk-cli", true},
		{"sdk-py", "sdk-py", true},
		{"sdk-ts", "sdk-ts", true},
		{"plain sdk", "sdk", true},
		{"external-sdk", "external-sdk", true},
		{"contains -sdk", "vendor-sdk-cli", true},
		{"uppercase sdk", "SDK-CLI", true},
		{"whitespace padded", "  sdk-cli  ", true},
		{"plain cli", "cli", false},
		{"vscode", "vscode", false},
		{"empty", "", false},
		{"sdk prefix matches without dash", "sdkx", true}, // documents the HasPrefix rule
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSDKEntrypoint(tt.ep); got != tt.want {
				t.Fatalf("IsSDKEntrypoint(%q) = %v, want %v", tt.ep, got, tt.want)
			}
		})
	}
}
