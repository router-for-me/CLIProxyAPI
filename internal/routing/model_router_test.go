package routing

import (
	"testing"
)

func TestExtractBaseModelName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Claude with date suffix",
			input:    "Claude-sonnet-20251124",
			expected: "Claude-sonnet",
		},
		{
			name:     "GPT with date suffix (YYYYMMDD)",
			input:    "gpt-4-turbo-20240409",
			expected: "gpt-4-turbo",
		},
		{
			name:     "Claude with version suffix",
			input:    "claude-3-5-sonnet-v1",
			expected: "claude-3-5-sonnet",
		},
		{
			name:     "Model with latest suffix",
			input:    "gpt-4-latest",
			expected: "gpt-4",
		},
		{
			name:     "Model with preview suffix",
			input:    "claude-opus-preview",
			expected: "claude-opus",
		},
		{
			name:     "Model with beta suffix",
			input:    "gemini-2-beta",
			expected: "gemini-2",
		},
		{
			name:     "Model with multiple version parts",
			input:    "claude-sonnet-4-20250514-v2",
			expected: "claude-sonnet-4",
		},
		{
			name:     "Simple model name without suffix",
			input:    "gpt-4o",
			expected: "gpt-4o",
		},
		{
			name:     "Single word model",
			input:    "gemini",
			expected: "gemini",
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "Whitespace only",
			input:    "   ",
			expected: "",
		},
		{
			name:     "Model with year only suffix",
			input:    "claude-sonnet-2024",
			expected: "claude-sonnet",
		},
		{
			name:     "Model with YYYYMM suffix",
			input:    "gpt-4-202501",
			expected: "gpt-4",
		},
		{
			name:     "Model name all version-like parts",
			input:    "v1-v2-v3",
			expected: "v1-v2-v3", // Should not remove everything
		},
		{
			name:     "Model with alpha suffix",
			input:    "claude-next-alpha",
			expected: "claude-next",
		},
		{
			name:     "Model with v2.0 suffix",
			input:    "gpt-4-v2.0",
			expected: "gpt-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBaseModelName(tt.input)
			if result != tt.expected {
				t.Errorf("extractBaseModelName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsDateLikeSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"20251124", true},  // YYYYMMDD
		{"2024", true},      // YYYY
		{"202501", true},    // YYYYMM
		{"123", false},      // Too short
		{"12345", false},    // Wrong length
		{"1234567", false},  // Wrong length
		{"123456789", false}, // Too long
		{"abcd", false},     // Not digits
		{"20ab", false},     // Mixed
		{"", false},         // Empty
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isDateLikeSuffix(tt.input)
			if result != tt.expected {
				t.Errorf("isDateLikeSuffix(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsVersionSuffix(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"v1", true},
		{"v2", true},
		{"v1.0", true},
		{"v2.5.1", true},
		{"V1", true},      // Case insensitive
		{"latest", true},
		{"LATEST", true},  // Case insensitive
		{"preview", true},
		{"beta", true},
		{"alpha", true},
		{"v", false},      // Too short
		{"version1", false},
		{"1.0", false},    // No 'v' prefix
		{"", false},
		{"vx", false},     // Invalid version
		{"v1a", false},    // Invalid version
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isVersionSuffix(tt.input)
			if result != tt.expected {
				t.Errorf("isVersionSuffix(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		text     string
		expected bool
	}{
		{
			name:     "Exact match",
			pattern:  "claude-sonnet",
			text:     "claude-sonnet",
			expected: true,
		},
		{
			name:     "Exact match case insensitive",
			pattern:  "Claude-Sonnet",
			text:     "claude-sonnet",
			expected: true,
		},
		{
			name:     "Single wildcard matches all",
			pattern:  "*",
			text:     "anything",
			expected: true,
		},
		{
			name:     "Prefix wildcard",
			pattern:  "*sonnet",
			text:     "claude-sonnet",
			expected: true,
		},
		{
			name:     "Suffix wildcard",
			pattern:  "claude-*",
			text:     "claude-sonnet-4",
			expected: true,
		},
		{
			name:     "Middle wildcard",
			pattern:  "claude-*-4",
			text:     "claude-sonnet-4",
			expected: true,
		},
		{
			name:     "Multiple wildcards",
			pattern:  "*claude*sonnet*",
			text:     "gemini-claude-3-sonnet-4",
			expected: true,
		},
		{
			name:     "No match - different text",
			pattern:  "claude-*",
			text:     "gpt-4",
			expected: false,
		},
		{
			name:     "No match - suffix mismatch",
			pattern:  "*-opus",
			text:     "claude-sonnet",
			expected: false,
		},
		{
			name:     "Empty pattern matches empty text",
			pattern:  "",
			text:     "",
			expected: true,
		},
		{
			name:     "Empty pattern does not match non-empty text",
			pattern:  "",
			text:     "something",
			expected: false,
		},
		{
			name:     "Contains pattern",
			pattern:  "*sonnet*",
			text:     "claude-sonnet-4-20250514",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchWildcard(tt.pattern, tt.text)
			if result != tt.expected {
				t.Errorf("matchWildcard(%q, %q) = %v, want %v", tt.pattern, tt.text, result, tt.expected)
			}
		})
	}
}
