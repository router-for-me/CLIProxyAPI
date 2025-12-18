package claude

import (
	"testing"
)

func TestConvertBashCommandToCmdField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic command to cmd conversion",
			input:    `{"command": "git diff"}`,
			expected: `{"cmd":"git diff"}`,
		},
		{
			name:     "already has cmd field - no change",
			input:    `{"cmd": "git diff"}`,
			expected: `{"cmd": "git diff"}`,
		},
		{
			name:     "both cmd and command - keep cmd only",
			input:    `{"command": "git diff", "cmd": "ls"}`,
			expected: `{"command": "git diff", "cmd": "ls"}`, // no change when cmd exists
		},
		{
			name:     "command with special characters in value",
			input:    `{"command": "echo \"command\": test"}`,
			expected: `{"cmd":"echo \"command\": test"}`,
		},
		{
			name:     "command with nested quotes",
			input:    `{"command": "bash -c 'echo \"hello\"'"}`,
			expected: `{"cmd":"bash -c 'echo \"hello\"'"}`,
		},
		{
			name:     "command with newlines",
			input:    `{"command": "echo hello\necho world"}`,
			expected: `{"cmd":"echo hello\necho world"}`,
		},
		{
			name:     "empty command value",
			input:    `{"command": ""}`,
			expected: `{"cmd":""}`,
		},
		{
			name:     "command with other fields - preserves them",
			input:    `{"command": "git diff", "timeout": 30}`,
			expected: `{ "timeout": 30,"cmd":"git diff"}`,
		},
		{
			name:     "invalid JSON - returns unchanged",
			input:    `{invalid json`,
			expected: `{invalid json`,
		},
		{
			name:     "empty object",
			input:    `{}`,
			expected: `{}`,
		},
		{
			name:     "no command field",
			input:    `{"restart": true}`,
			expected: `{"restart": true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertBashCommandToCmdField(tt.input)
			if result != tt.expected {
				t.Errorf("convertBashCommandToCmdField(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
