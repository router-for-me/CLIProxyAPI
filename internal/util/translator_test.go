package util

import (
	"testing"
)

func TestNormalizeReasoningContent(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "single newline preserved",
			input:    "Line 1\nLine 2",
			expected: "Line 1\nLine 2",
		},
		{
			name:     "standard two newlines preserved",
			input:    "Paragraph 1\n\nParagraph 2",
			expected: "Paragraph 1\n\nParagraph 2",
		},
		{
			name:     "triple newline collapsed to double newline",
			input:    "Step 1 finished.\n\n\n",
			expected: "Step 1 finished.\n\n",
		},
		{
			name:     "quadruple newline collapsed to double newline",
			input:    "Step 1\n\n\n\nStep 2",
			expected: "Step 1\n\nStep 2",
		},
		{
			name:     "CRLF triple newline collapsed to double newline",
			input:    "Step 1\r\n\r\n\r\nStep 2",
			expected: "Step 1\n\nStep 2",
		},
		{
			name:     "multiple excessive newlines across paragraphs",
			input:    "Step 1.\n\n\nStep 2.\n\n\n\nStep 3.\n\n",
			expected: "Step 1.\n\nStep 2.\n\nStep 3.\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeReasoningContent(tt.input)
			if got != tt.expected {
				t.Errorf("NormalizeReasoningContent(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
