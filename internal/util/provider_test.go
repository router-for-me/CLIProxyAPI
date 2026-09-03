package util

import (
	"reflect"
	"testing"
)

func TestGetProviderName(t *testing.T) {
	tests := []struct {
		model    string
		expected []string
	}{
		{
			model:    "gemini-3.7-flash-high",
			expected: []string{"antigravity"},
		},
		{
			model:    "gemini-2.5-pro",
			expected: []string{"gemini", "gemini-interactions", "vertex", "aistudio"},
		},
		{
			model:    "claude-sonnet-4-6",
			expected: []string{"claude", "antigravity"},
		},
		{
			model:    "claude-sonnet-4-5",
			expected: []string{"claude"},
		},
		{
			model:    "gpt-5",
			expected: []string{"codex"},
		},
		{
			model:    "chatgpt-4o-latest",
			expected: []string{"codex"},
		},
		{
			model:    "o1-preview",
			expected: []string{"codex"},
		},
		{
			model:    "o3-mini",
			expected: []string{"codex"},
		},
		{
			model:    "text-embedding-3-small",
			expected: []string{"codex"},
		},
		{
			model:    "grok-3",
			expected: []string{"xai"},
		},
		{
			model:    "xai-grok-2",
			expected: []string{"xai"},
		},
		{
			model:    "kimi-k1.5",
			expected: []string{"kimi"},
		},
		{
			model:    "moonshot-v1-8k",
			expected: []string{"kimi"},
		},
		{
			model:    "deepseek-chat",
			expected: []string{"codex", "claude"},
		},
		{
			model:    "completely-unrecognized-model",
			expected: nil,
		},
		{
			model:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := GetProviderName(tt.model)
			if len(got) == 0 && len(tt.expected) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("GetProviderName(%q) = %v, want %v", tt.model, got, tt.expected)
			}
		})
	}
}
