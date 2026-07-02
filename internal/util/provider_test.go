package util

import "testing"

func TestIsOpenAICompatibleProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     bool
	}{
		{name: "empty", provider: "", want: false},
		{name: "whitespace", provider: "   ", want: false},
		{name: "generic", provider: "openai-compatibility", want: true},
		{name: "generic mixed case", provider: " OpenAI-Compatibility ", want: true},
		{name: "named provider", provider: "openai-compatible-kimi", want: true},
		{name: "named provider mixed case", provider: " OpenAI-Compatible-Kimi ", want: true},
		{name: "unrelated provider", provider: "openai", want: false},
		{name: "prefix lookalike", provider: "openai-compat-kimi", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsOpenAICompatibleProvider(tt.provider); got != tt.want {
				t.Fatalf("IsOpenAICompatibleProvider(%q) = %v, want %v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestOpenAICompatibleProviderName(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		want     string
	}{
		{name: "empty", provider: "", want: ""},
		{name: "generic", provider: "openai-compatibility", want: ""},
		{name: "named provider", provider: "openai-compatible-kimi", want: "kimi"},
		{name: "named provider mixed case", provider: " OpenAI-Compatible-Kimi ", want: "kimi"},
		{name: "unrelated provider", provider: "openai", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OpenAICompatibleProviderName(tt.provider); got != tt.want {
				t.Fatalf("OpenAICompatibleProviderName(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}
