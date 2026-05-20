package util

import (
	"net/http"
	"testing"
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

func TestIsForceFastModeHeader(t *testing.T) {
	tests := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"1", "1", true, true},
		{"true lowercase", "true", true, true},
		{"True mixed case", "True", true, true},
		{"TRUE uppercase", "TRUE", true, true},
		{"yes", "yes", true, true},
		{"on", "on", true, true},
		{"whitespace padded true", "  true  ", true, true},
		{"whitespace padded 1", " 1 ", true, true},

		{"0 is false", "0", true, false},
		{"false is false", "false", true, false},
		{"no is false", "no", true, false},
		{"off is false", "off", true, false},
		{"random string is false", "maybe", true, false},
		{"empty value is false", "", true, false},
		{"header absent is false", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.set {
				h.Set("X-CPA-Force-Fast-Mode", tt.value)
			}
			if got := IsForceFastModeHeader(h); got != tt.want {
				t.Fatalf("IsForceFastModeHeader header=%q set=%v = %v, want %v", tt.value, tt.set, got, tt.want)
			}
		})
	}

	t.Run("nil header returns false", func(t *testing.T) {
		if IsForceFastModeHeader(nil) != false {
			t.Fatal("IsForceFastModeHeader(nil) should be false")
		}
	})

	t.Run("canonical header lookup is case-insensitive", func(t *testing.T) {
		// http.Header.Get is case-insensitive via canonicalization; this test
		// guards against a future regression if someone changes the helper to
		// use direct map access.
		h := http.Header{}
		h.Set("x-cpa-force-fast-mode", "1")
		if !IsForceFastModeHeader(h) {
			t.Fatal("IsForceFastModeHeader should match canonical header form")
		}
	})
}
