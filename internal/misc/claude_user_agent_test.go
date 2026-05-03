package misc

import "testing"

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
