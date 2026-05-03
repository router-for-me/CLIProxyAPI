package misc

import (
	"net/http"
	"testing"
)

func TestIsClaudeCompatibleUserAgent(t *testing.T) {
	testCases := []struct {
		name      string
		userAgent string
		want      bool
	}{
		{
			name:      "claude cli",
			userAgent: "claude-cli/2.1.70 (external, cli)",
			want:      true,
		},
		{
			name:      "anthropic js sdk",
			userAgent: "Anthropic/JS 0.91.1",
			want:      true,
		},
		{
			name:      "claude code",
			userAgent: "Claude Code/2.0",
			want:      true,
		},
		{
			name:      "curl",
			userAgent: "curl/8.7.1",
			want:      false,
		},
		{
			name:      "empty",
			userAgent: "",
			want:      false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClaudeCompatibleUserAgent(tc.userAgent); got != tc.want {
				t.Fatalf("IsClaudeCompatibleUserAgent(%q) = %v, want %v", tc.userAgent, got, tc.want)
			}
		})
	}
}

func TestIsClaudeCompatibleHeaders(t *testing.T) {
	testCases := []struct {
		name    string
		headers http.Header
		want    bool
	}{
		{
			name: "anthropic user agent",
			headers: http.Header{
				"User-Agent": []string{"Anthropic/JS 0.91.1"},
			},
			want: true,
		},
		{
			name: "x api key",
			headers: http.Header{
				"X-Api-Key": []string{"test-key"},
			},
			want: true,
		},
		{
			name: "anthropic version",
			headers: http.Header{
				"Anthropic-Version": []string{"2023-06-01"},
			},
			want: true,
		},
		{
			name: "anthropic beta",
			headers: http.Header{
				"Anthropic-Beta": []string{"claude-code-20250219"},
			},
			want: true,
		},
		{
			name: "direct browser access",
			headers: http.Header{
				"Anthropic-Dangerous-Direct-Browser-Access": []string{"true"},
			},
			want: true,
		},
		{
			name: "openai authorization",
			headers: http.Header{
				"Authorization": []string{"Bearer test-key"},
				"User-Agent":    []string{"curl/8.7.1"},
			},
			want: false,
		},
		{
			name:    "empty",
			headers: http.Header{},
			want:    false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClaudeCompatibleHeaders(tc.headers); got != tc.want {
				t.Fatalf("IsClaudeCompatibleHeaders(%v) = %v, want %v", tc.headers, got, tc.want)
			}
		})
	}
}
