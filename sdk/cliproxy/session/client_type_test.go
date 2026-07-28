package session

import (
	"net/http"
	"testing"
)

// User-Agent values below are the ones captured from the real clients.
func TestDetectClientType(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{
			name: "claude code cli sends corroborating signals",
			headers: http.Header{
				"User-Agent":               {"claude-cli/2.1.193 (external, sdk-cli)"},
				"X-App":                    {"cli"},
				"Anthropic-Beta":           {"claude-code-20250219,interleaved-thinking-2025-05-14"},
				"X-Claude-Code-Session-Id": {"11111111-1111-4111-8111-111111111111"},
			},
			want: ClientTypeClaudeCode,
		},
		{
			name: "claude code without the cli user agent still matches on two signals",
			headers: http.Header{
				"X-App":                    {"cli"},
				"Anthropic-Beta":           {"claude-code-20250219"},
				"X-Claude-Code-Session-Id": {"11111111-1111-4111-8111-111111111111"},
			},
			want: ClientTypeClaudeCode,
		},
		{
			name:    "claude cli user agent alone is not enough",
			headers: http.Header{"User-Agent": {"claude-cli/2.1.193 (external, sdk-cli)"}},
			want:    "",
		},
		{
			// Background requests report cli-bg. With a stripped User-Agent the
			// X-App/Anthropic-Beta pair is one of only two available signals, so a
			// strict "cli" comparison would drop detection to a single signal.
			name: "claude code background requests report cli-bg",
			headers: http.Header{
				"X-App":                    {"cli-bg"},
				"Anthropic-Beta":           {"claude-code-20250219"},
				"X-Claude-Code-Session-Id": {"11111111-1111-4111-8111-111111111111"},
			},
			want: ClientTypeClaudeCode,
		},
		{
			name: "anthropic sdk is not claude code",
			headers: http.Header{
				"User-Agent":        {"Anthropic/Python 0.40.0"},
				"Anthropic-Version": {"2023-06-01"},
				"X-Stainless-Lang":  {"python"},
			},
			want: ClientTypeAnthropicSDK,
		},
		{
			name:    "codex cli",
			headers: http.Header{"User-Agent": {"codex_exec/0.144.6"}},
			want:    ClientTypeCodex,
		},
		{
			name:    "codex identified by its own header",
			headers: http.Header{"X-Codex-Turn-Metadata": {`{"turn_id":"t"}`}},
			want:    ClientTypeCodex,
		},
		{
			name:    "opencode cli",
			headers: http.Header{"User-Agent": {"opencode/1.18.3 ai-sdk/provider-utils/3.0.1 runtime/bun/1.2"}},
			want:    ClientTypeOpenCode,
		},
		{
			name:    "vercel ai sdk",
			headers: http.Header{"User-Agent": {"ai/5.0.26 ai-sdk/provider-utils/3.0.1 runtime/node.js/22"}},
			want:    ClientTypeVercelAI,
		},
		{
			name:    "openai sdk",
			headers: http.Header{"User-Agent": {"OpenAI/JS 6.26.0"}},
			want:    ClientTypeOpenAISDK,
		},
		{
			name:    "google gen ai sdk",
			headers: http.Header{"X-Goog-Api-Client": {"google-genai-sdk/1.0.0 gl-python/3.12"}},
			want:    ClientTypeGoogleGenAI,
		},
		{
			name:    "declared gateway client wins",
			headers: http.Header{"X-Gateway-Client": {"Kimi-Code"}, "User-Agent": {"OpenAI/JS 6.26.0"}},
			want:    "kimi-code",
		},
		{
			name:    "unknown client is not guessed",
			headers: http.Header{"User-Agent": {"curl/8.4.0"}},
			want:    "",
		},
		{
			name:    "no headers",
			headers: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectClientType(tt.headers); got != tt.want {
				t.Fatalf("DetectClientType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectClientTypeRejectsUnsafeDeclaredValue(t *testing.T) {
	headers := http.Header{"X-Gateway-Client": {"bad\nvalue"}, "User-Agent": {"opencode/1.18.3"}}

	if got := DetectClientType(headers); got != ClientTypeOpenCode {
		t.Fatalf("DetectClientType() = %q, want %q", got, ClientTypeOpenCode)
	}
}
