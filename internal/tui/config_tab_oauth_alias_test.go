package tui

import "testing"

func TestSummarizeOAuthModelAlias_CountsOrderedPools(t *testing.T) {
	raw := map[string]any{
		"codex": []any{
			map[string]any{"name": "gpt-5", "alias": "g5"},
			map[string]any{"name": "gpt-5-mini", "alias": "g5"},
			map[string]any{"name": "gpt-6", "alias": "g6"},
		},
		"claude": []any{
			map[string]any{"name": "claude-opus-4", "alias": "opus"},
		},
	}
	channels, ordered := summarizeOAuthModelAlias(raw)
	if channels != 2 {
		t.Fatalf("channels = %d, want 2", channels)
	}
	if ordered != 1 {
		t.Fatalf("ordered aliases = %d, want 1", ordered)
	}
}

func TestSummarizeOAuthModelAlias_Empty(t *testing.T) {
	channels, ordered := summarizeOAuthModelAlias(nil)
	if channels != 0 || ordered != 0 {
		t.Fatalf("empty summary = %d, %d; want 0, 0", channels, ordered)
	}
}
