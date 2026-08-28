package responses

import "testing"

// Anthropic reads a trailing assistant message as prefill, and the current
// Claude generation rejects it: "This model does not support assistant message
// prefill. The conversation must end with a user message." The Responses API
// has no prefill concept, so a trailing assistant item is replayed history,
// not a directive. Measured against Anthropic: claude-opus-5, claude-sonnet-4-6
// and claude-fable-5 reject it, claude-haiku-4-5 accepts it. Only the Fable
// name was handled, so every Codex compaction turn on Opus and Sonnet failed.
func TestTrailingAssistantPrefillDroppedForModelsThatRejectIt(t *testing.T) {
	raw := responsesRequestFromItems(
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}`,
	)

	for _, tc := range []struct {
		model       string
		wantDropped bool
	}{
		{"claude-fable-5", true},
		{"claude-opus-5", true},
		{"claude-sonnet-4-6", true},
		{"claude-haiku-4-5-20251001", false},
	} {
		out := ConvertOpenAIResponsesRequestToClaude(tc.model, raw, false)
		last := lastClaudeMessageRole(t, out)
		if tc.wantDropped && last == "assistant" {
			t.Fatalf("%s: conversation still ends with an assistant message. Output: %s", tc.model, string(out))
		}
		if !tc.wantDropped && last != "assistant" {
			t.Fatalf("%s: prefill was dropped for a model that accepts it. Output: %s", tc.model, string(out))
		}
	}
}
