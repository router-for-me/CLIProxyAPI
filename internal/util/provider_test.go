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
		{"gemini-3.7-flash-high", []string{"antigravity"}},
		{"gemini-2.5-pro", []string{"gemini", "gemini-interactions", "vertex", "aistudio"}},
		{"claude-sonnet-4-6", []string{"claude", "antigravity"}},
		{"claude-sonnet-4-5", []string{"claude"}},
		{"gpt-5", []string{"codex"}},
		{"o3-mini", []string{"codex"}},
		{"grok-3", []string{"xai"}},
		{"kimi-k1.5", []string{"kimi"}},
		{"deepseek-chat", []string{"codex", "claude"}},
		{"unknown-model", nil},
		{"", nil},
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
