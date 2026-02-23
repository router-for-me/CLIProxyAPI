//go:build skip
// +build skip

package gemini

import (
	"reflect"
	"testing"
)

func TestFilterGeminiModelFields(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected map[string]any
	}{
		{
			name: "filter out internal metadata fields",
			input: map[string]any{
				"name":                       "models/gemini-2.5-pro",
				"version":                    "2.5",
				"displayName":                "Gemini 2.5 Pro",
				"description":                "Test model",
				"inputTokenLimit":            1000000,
				"outputTokenLimit":           65536,
				"supportedGenerationMethods": []string{"generateContent"},
				// These internal fields should be filtered out
				"id":                    "gemini-2.5-pro",
				"object":                "model",
				"created":               int64(1750118400),
				"owned_by":              "google",
				"type":                  "gemini",
				"context_length":        1000000,
				"max_completion_tokens": 65536,
				"thinking":              map[string]any{"min": 128, "max": 32768},
			},
			expected: map[string]any{
				"name":                       "models/gemini-2.5-pro",
				"version":                    "2.5",
				"displayName":                "Gemini 2.5 Pro",
				"description":                "Test model",
				"inputTokenLimit":            1000000,
				"outputTokenLimit":           65536,
				"supportedGenerationMethods": []string{"generateContent"},
			},
		},
		{
			name: "include temperature, topP, topK",
			input: map[string]any{
				"name":        "models/test-model",
				"temperature": 0.7,
				"topP":        0.9,
				"topK":        40,
				// Should be filtered
				"id":       "test-model",
				"thinking": true,
			},
			expected: map[string]any{
				"name":        "models/test-model",
				"temperature": 0.7,
				"topP":        0.9,
				"topK":        40,
			},
		},
		{
			name:     "empty input",
			input:    map[string]any{},
			expected: map[string]any{},
		},
		{
			name: "all fields should be filtered",
			input: map[string]any{
				"id":             "test",
				"object":         "model",
				"created":        int64(12345),
				"owned_by":       "test",
				"type":           "test",
				"thinking":       true,
				"context_length": 1000,
			},
			expected: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterGeminiModelFields(tt.input)

			// Check that all expected fields are present
			for k, v := range tt.expected {
				if !reflect.DeepEqual(result[k], v) {
					t.Errorf("expected %s = %v, got %v", k, v, result[k])
				}
			}

			// Check that no extra fields are present
			if len(result) != len(tt.expected) {
				t.Errorf("expected %d fields, got %d", len(tt.expected), len(result))
				for k := range result {
					if _, ok := tt.expected[k]; !ok {
						t.Errorf("unexpected field: %s", k)
					}
				}
			}
		})
	}
}
