package handlers

import (
	"testing"
)

func TestMaybeFallbackClaudeOpusModel(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude-opus-4-8", "claude-opus-4-6"},
		{"claude-opus-4-7", "claude-opus-4-6"},
		{"claude-opus-4-6", ""},
		{"claude-opus-4-8(high)", "claude-opus-4-6(high)"},
		{"claude-opus-4-7(16384)", "claude-opus-4-6(16384)"},
		{"gpt-5.5", ""},
		{"claude-sonnet-4-5", ""},
		{"claude-opus-4-8-20250801", "claude-opus-4-6"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := maybeFallbackClaudeOpusModel(tt.input)
			if got != tt.expected {
				t.Errorf("maybeFallbackClaudeOpusModel(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestWithFallbackModelInPayload(t *testing.T) {
	tests := []struct {
		name          string
		rawJSON       []byte
		fallbackModel string
		expected      string
	}{
		{
			name:          "empty payload",
			rawJSON:       []byte{},
			fallbackModel: "claude-opus-4-6",
			expected:      "",
		},
		{
			name:          "no model field",
			rawJSON:       []byte(`{"messages":[]}`),
			fallbackModel: "claude-opus-4-6",
			expected:      `{"messages":[]}`,
		},
		{
			name:          "replace model",
			rawJSON:       []byte(`{"model":"claude-opus-4-8","messages":[]}`),
			fallbackModel: "claude-opus-4-6",
			expected:      `{"model":"claude-opus-4-6","messages":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withFallbackModelInPayload(tt.rawJSON, tt.fallbackModel)
			if string(got) != tt.expected {
				t.Errorf("withFallbackModelInPayload() = %s, want %s", got, tt.expected)
			}
		})
	}
}
func TestRestoreOriginalModelInBody(t *testing.T) {
	tests := []struct {
		name          string
		body          []byte
		originalModel string
		expected      string
	}{
		{
			name:          "empty body",
			body:          []byte{},
			originalModel: "claude-opus-4-8",
			expected:      "",
		},
		{
			name:          "no model field",
			body:          []byte(`{"id":"123"}`),
			originalModel: "claude-opus-4-8",
			expected:      `{"id":"123"}`,
		},
		{
			name:          "replace model",
			body:          []byte(`{"model":"claude-opus-4-6","id":"123"}`),
			originalModel: "claude-opus-4-8",
			expected:      `{"model":"claude-opus-4-8","id":"123"}`,
		},
		{
			name:          "empty original model",
			body:          []byte(`{"model":"claude-opus-4-6"}`),
			originalModel: "",
			expected:      `{"model":"claude-opus-4-6"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restoreOriginalModelInBody(tt.body, tt.originalModel)
			if string(got) != tt.expected {
				t.Errorf("restoreOriginalModelInBody() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func TestRestoreOriginalModelInChunk(t *testing.T) {
	tests := []struct {
		name          string
		chunk         []byte
		originalModel string
		expected      string
	}{
		{
			name:          "empty chunk",
			chunk:         []byte{},
			originalModel: "claude-opus-4-8",
			expected:      "",
		},
		{
			name:          "no model field",
			chunk:         []byte(`{"choices":[]}`),
			originalModel: "claude-opus-4-8",
			expected:      `{"choices":[]}`,
		},
		{
			name:          "replace model",
			chunk:         []byte(`{"model":"claude-opus-4-6","choices":[]}`),
			originalModel: "claude-opus-4-8",
			expected:      `{"model":"claude-opus-4-8","choices":[]}`,
		},
		{
			name:          "with thinking suffix",
			chunk:         []byte(`{"model":"claude-opus-4-6(high)"}`),
			originalModel: "claude-opus-4-8(high)",
			expected:      `{"model":"claude-opus-4-8(high)"}`,
		},
		{
			name:          "empty original model",
			chunk:         []byte(`{"model":"claude-opus-4-6"}`),
			originalModel: "",
			expected:      `{"model":"claude-opus-4-6"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := restoreOriginalModelInChunk(tt.chunk, tt.originalModel)
			if string(got) != tt.expected {
				t.Errorf("restoreOriginalModelInChunk() = %s, want %s", got, tt.expected)
			}
		})
	}
}
