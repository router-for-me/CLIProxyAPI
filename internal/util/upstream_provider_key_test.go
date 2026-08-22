package util

import "testing"

func TestUpstreamProviderKey(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		providerName string
		want         string
	}{
		{"openai-compatibility with name", "openai-compatibility", "opencode", "openai-compatible-opencode"},
		{"openai-compatibility empty name", "openai-compatibility", "", "openai-compatibility"},
		{"openai-compatibility with uppercase name", "openai-compatibility", "OpenCode", "openai-compatible-opencode"},
		{"openai-compatibility already prefixed", "openai-compatibility", "openai-compatible-opencode", "openai-compatible-opencode"},
		{"oauth claude", "oauth:claude", "anything", "claude"},
		{"oauth codex", "oauth:codex", "anything", "codex"},
		{"oauth aistudio", "oauth:aistudio", "anything", "aistudio"},
		// Built-in -api-key channels always use the fixed channel name (the
		// row's `name` is just an identifier label). Regression for the
		// per-model routing picker, which depends on this key matching
		// the live provider key returned by registry.GetModelProviders.
		{"claude-api-key ignores name", "claude-api-key", "minimax", "claude"},
		{"gemini-api-key ignores name", "gemini-api-key", "MyGeminiKey", "gemini"},
		{"codex-api-key ignores name", "codex-api-key", "teamA", "codex"},
		{"xai-api-key ignores name", "xai-api-key", "team-b", "xai"},
		{"vertex-api-key ignores name", "vertex-api-key", "team-c", "vertex"},
		{"interactions-api-key ignores name", "interactions-api-key", "team-d", "interactions"},
		{"-api suffix (legacy) maps to channel", "claude-api", "team-e", "claude"},
		{"claude-api-key with blank name", "claude-api-key", "", "claude"},
		{"empty providerType falls back to openai-compatible key", "", "opencode", "openai-compatible-opencode"},
		{"unknown providerType falls back to lowercased name", "custom-kind", "FooBar", "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UpstreamProviderKey(tc.providerType, tc.providerName)
			if got != tc.want {
				t.Fatalf("UpstreamProviderKey(%q, %q) = %q, want %q", tc.providerType, tc.providerName, got, tc.want)
			}
		})
	}
}
