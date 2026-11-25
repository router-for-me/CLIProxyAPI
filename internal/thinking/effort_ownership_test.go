package thinking

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"
)

func stringBackedBySlice(s string, buf []byte) bool {
	if len(s) == 0 || len(buf) == 0 {
		return false
	}
	data := unsafe.StringData(s)
	target := []byte(s)
	for offset := 0; offset <= len(buf)-len(target); {
		idx := bytes.Index(buf[offset:], target)
		if idx < 0 {
			return false
		}
		pos := offset + idx
		if data == &buf[pos] {
			return true
		}
		offset = pos + 1
	}
	return false
}

func TestUsageEffortDoesNotRetainRequestBuffer(t *testing.T) {
	padding := strings.Repeat("x", 1<<20)

	cases := []struct {
		name     string
		template string
		want     string
		extract  func([]byte) string
	}{
		{
			name:     "Claude request",
			template: `{"output_config":{"effort":"high"},"content":"%s"}`,
			want:     "high",
			extract:  func(b []byte) string { return ExtractReasoningEffort(b, "claude", "gpt-5.4") },
		},
		{
			name:     "Claude mid-conversation directive",
			template: `{"output_config":{"effort":"low"},"messages":[{"role":"system","output_config":{"effort":"medium"}},{"role":"user","content":"%s"}]}`,
			want:     "medium",
			extract:  func(b []byte) string { return ExtractReasoningEffort(b, "claude", "claude-opus-4-6") },
		},
		{
			name:     "Codex wire request",
			template: `{"reasoning":{"effort":"high"},"input":"%s"}`,
			want:     "high",
			extract:  func(b []byte) string { return ExtractTranslatedReasoningEffort(b, "codex") },
		},
		{
			name:     "Codex configuration_update request",
			template: `{"reasoning":{"effort":"low"},"input":[{"type":"configuration_update","reasoning":{"effort":"xhigh"}},{"type":"message","content":"%s"}]}`,
			want:     "xhigh",
			extract:  func(b []byte) string { return ExtractReasoningEffort(b, "openai-response", "gpt-5.4") },
		},
		{
			name:     "OpenAI chat request",
			template: `{"reasoning_effort":"medium","messages":[{"role":"user","content":"%s"}]}`,
			want:     "medium",
			extract:  func(b []byte) string { return ExtractReasoningEffort(b, "openai", "gpt-5.4") },
		},
		{
			name:     "Gemini translated request",
			template: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}},"contents":"%s"}`,
			want:     "high",
			extract:  func(b []byte) string { return ExtractTranslatedReasoningEffort(b, "gemini") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(strings.Replace(tc.template, "%s", padding, 1))
			effort := tc.extract(body)
			if effort != tc.want {
				t.Fatalf("effort = %q, want %q", effort, tc.want)
			}
			if stringBackedBySlice(effort, body) {
				t.Fatalf("returned effort %q points into the %d-byte JSON buffer", effort, len(body))
			}
		})
	}
}
