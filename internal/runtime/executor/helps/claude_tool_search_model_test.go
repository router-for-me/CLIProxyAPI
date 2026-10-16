package helps

import "testing"

func TestClaudeModelLacksToolSearch(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-1-20250805",
		"anthropic/claude-3-7-sonnet-20250219",
		"claude-sonnet-4-20250514(high)",
		"claude-sonnet-4[1m]",
		"CLAUDE-OPUS-4",
		"team/claude-opus-4-1",
		"claude-opus-4-0",
		"claude-sonnet-4-0",
		"claude-3-5-haiku-20241022",
		"claude-2.1",
		"claude-instant-1.2",
	} {
		if !ClaudeModelLacksToolSearch(model) {
			t.Errorf("ClaudeModelLacksToolSearch(%q) = false, want true", model)
		}
	}
	for _, model := range []string{
		"claude-sonnet-4-5",
		"claude-sonnet-4-5-20250929",
		"claude-haiku-4-5-20251001",
		"claude-opus-4-5-20251101",
		"claude-opus-4-6",
		"claude-fable-5-1",
		"glm-4.6",
		"",
	} {
		if ClaudeModelLacksToolSearch(model) {
			t.Errorf("ClaudeModelLacksToolSearch(%q) = true, want false", model)
		}
	}
}
