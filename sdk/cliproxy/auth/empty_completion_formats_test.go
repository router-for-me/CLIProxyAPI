package auth

import (
	"testing"
)

// TestSupportedCompletionFormatsRecognized covers representative wire formats
// handled by empty-completion detection. Executor names document current users
// of each format; this manually maintained table is not an executor-registry
// completeness check.
//
// Each case lists executors that emit a given wire format plus a
// representative NON-empty chunk in that format (asserted recognized=true) and
// the corresponding empty-terminal variant (asserted empty).
//
// Documented exclusions (NOT in this table, by design):
//   - codex-live: realtime bidirectional voice/media relay, not a text
//     completion stream — empty-completion handling does not apply.
//   - gemini-interactions: Gemini Live realtime relay (parts/role frames, not
//     candidates-shaped) — voice/media channel, not a text completion stream.
func TestSupportedCompletionFormatsRecognized(t *testing.T) {
	cases := []struct {
		name      string
		executors []string
		nonEmpty  []byte
		empty     []byte
	}{
		{
			// OpenAI chat-completions wire. Emitted by the OpenAI-compatible
			// proxy executors (their requestToFormat is FormatOpenAI).
			name:      "openai-chat",
			executors: []string{"kimi", "kiro", "kilo", "cursor", "github-copilot", "codebuddy", "gitlab", "qoder", "openai-compatibility"},
			nonEmpty:  []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"),
			empty:     []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":0}}\n\ndata: [DONE]\n\n"),
		},
		{
			// OpenAI Responses-API wire (codex agent format). Emitted by the
			// codex-family executors (requestToFormat is FormatCodex).
			name:      "codex-responses",
			executors: []string{"codex", "home_codex", "xai"},
			nonEmpty:  []byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"output_tokens\":5}}}\n\ndata: [DONE]\n\n"),
			empty:     []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[],\"usage\":{\"output_tokens\":0}}}\n\ndata: [DONE]\n\n"),
		},
		{
			// Anthropic Claude wire. Emitted by the Claude executor
			// (requestToFormat is FormatClaude).
			name:      "claude",
			executors: []string{"claude"},
			nonEmpty:  []byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
			empty:     []byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"),
		},
		{
			// Gemini wire (top-level candidates). Emitted by the Gemini-family
			// executors (requestToFormat is FormatGemini). aistudio is listed
			// here as its representative case: it emits the client-requested
			// SDK format (body.toFormat), and Gemini is one of its valid
			// outputs — all of which are recognized formats.
			name:      "gemini",
			executors: []string{"gemini", "gemini-cli", "vertex", "aistudio"},
			nonEmpty:  []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":5}}\n\n"),
			empty:     []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"candidatesTokenCount\":0}}\n\n"),
		},
		{
			// Antigravity emits Gemini-shaped wire (nested response.candidates
			// wrapper), recognized through the same Gemini predicate.
			name:      "antigravity",
			executors: []string{"antigravity"},
			nonEmpty:  []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]},\"finishReason\":\"STOP\"}]}}\n\n"),
			empty:     []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[]},\"finishReason\":\"STOP\"}]}}\n\n"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !IsCompletionFormatRecognized(tc.nonEmpty) {
				t.Fatalf("non-empty chunk for executors %v was NOT recognized; a new executor emitting this format would silently bypass empty-completion detection", tc.executors)
			}
			if !IsEmptyCompletionPayload(tc.empty) {
				t.Fatalf("empty-terminal variant for executors %v was not judged empty", tc.executors)
			}
			if IsEmptyCompletionPayload(tc.nonEmpty) {
				t.Fatalf("non-empty chunk for executors %v was wrongly judged empty", tc.executors)
			}
		})
	}
}
