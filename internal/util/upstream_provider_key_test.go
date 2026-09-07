package util

import "testing"

func TestUpstreamProviderKey(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		providerName string
		rowID        int64
		want         string
	}{
		{"openai-compatibility with name", "openai-compatibility", "opencode", 0, "openai-compatible-opencode"},
		{"openai-compatibility empty name", "openai-compatibility", "", 0, "openai-compatibility"},
		{"openai-compatibility with uppercase name", "openai-compatibility", "OpenCode", 0, "openai-compatible-opencode"},
		{"openai-compatibility already prefixed", "openai-compatibility", "openai-compatible-opencode", 0, "openai-compatible-opencode"},
		{"oauth claude", "oauth:claude", "anything", 0, "claude"},
		{"oauth codex", "oauth:codex", "anything", 0, "codex"},
		{"oauth aistudio", "oauth:aistudio", "anything", 0, "aistudio"},
		// rowID==0 → bare channel (legacy behaviour; each upstream in the
		// picker collapses onto one routing key, kept for callers without
		// a stable row id).
		{"claude-api-key rowID=0 ignores name", "claude-api-key", "minimax", 0, "claude"},
		{"gemini-api-key rowID=0 ignores name", "gemini-api-key", "MyGeminiKey", 0, "gemini"},
		{"codex-api-key rowID=0 ignores name", "codex-api-key", "teamA", 0, "codex"},
		{"xai-api-key rowID=0 ignores name", "xai-api-key", "team-b", 0, "xai"},
		{"vertex-api-key rowID=0 ignores name", "vertex-api-key", "team-c", 0, "vertex"},
		{"interactions-api-key rowID=0 maps to gemini-interactions", "interactions-api-key", "team-d", 0, "gemini-interactions"},
		{"-api suffix (legacy) rowID=0 maps to channel", "claude-api", "team-e", 0, "claude"},
		{"claude-api-key rowID=0 with blank name", "claude-api-key", "", 0, "claude"},
		// rowID>0 → per-row routing key. Each upstream is now individually
		// selectable in the per-model routing picker, which is the user
		// request behind v7.2.138-0.1.2.
		{"claude-api-key rowID=42 uses suffix", "claude-api-key", "anthropic", 42, "claude:42"},
		{"claude-api-key rowID=1 uses suffix", "claude-api-key", "minimax", 1, "claude:1"},
		{"gemini-api-key rowID=7 uses suffix", "gemini-api-key", "prod", 7, "gemini:7"},
		{"codex-api-key rowID=9 uses suffix", "codex-api-key", "teamA", 9, "codex:9"},
		{"vertex-api-key rowID=3 uses suffix", "vertex-api-key", "", 3, "vertex:3"},
		{"interactions-api-key rowID=5 uses gemini-interactions prefix", "interactions-api-key", "", 5, "gemini-interactions:5"},
		{"-api suffix (legacy) rowID>0 also suffixes", "claude-api", "team-e", 11, "claude:11"},
		{"openai-compatibility rowID is ignored", "openai-compatibility", "opencode", 99, "openai-compatible-opencode"},
		{"empty providerType falls back to openai-compatible key", "", "opencode", 0, "openai-compatible-opencode"},
		{"unknown providerType falls back to lowercased name", "custom-kind", "FooBar", 0, "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UpstreamProviderKey(tc.providerType, tc.providerName, tc.rowID)
			if got != tc.want {
				t.Fatalf("UpstreamProviderKey(%q, %q, %d) = %q, want %q", tc.providerType, tc.providerName, tc.rowID, got, tc.want)
			}
		})
	}
}

func TestUpstreamProviderKeyOpenCodeGo(t *testing.T) {
	if got := UpstreamProviderKey("opencode-go", "ocgo", 7); got != "opencode-go:7" {
		t.Fatalf("got %q, want opencode-go:7", got)
	}
	if got := UpstreamProviderKey("opencode-go", "ocgo", 0); got != "opencode-go" {
		t.Fatalf("legacy bare key: got %q, want opencode-go", got)
	}
}
