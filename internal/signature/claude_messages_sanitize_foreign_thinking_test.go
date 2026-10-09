package signature

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeClaudeMessagesForClaudeUpstream_DroppedForeignProviderThinking(t *testing.T) {
	geminiCarrier := testGeminiThoughtSignatureEnvelope()
	cais := testClaudeCAISSignature("claude-fable-5")

	cases := []struct {
		name     string
		input    []byte
		wantFlag bool
		wantDrop int
		wantKeep int
	}{
		{
			name: "foreign unsigned non-empty thinking",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"foreign reasoning"},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: true,
			wantDrop: 1,
		},
		{
			name: "gemini-carrier incompatible signature",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"gemini thought","signature":"` + geminiCarrier + `"},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: true,
			wantDrop: 1,
		},
		{
			name: "empty placeholder drop only",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":""},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: false,
			wantDrop: 1,
		},
		{
			name: "legal Claude CAIS only",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"keep","signature":"` + cais + `"},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: false,
			wantKeep: 1,
		},
		{
			name: "empty placeholder plus legal CAIS does not count as foreign",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":""},
				{"type":"thinking","thinking":"keep","signature":"` + cais + `"},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: false,
			wantDrop: 1,
			wantKeep: 1,
		},
		{
			name: "unsigned empty text with thoughtSignature is foreign",
			input: []byte(`{"messages":[{"role":"assistant","content":[
				{"type":"thinking","thinking":"","thoughtSignature":"skip_thought_signature_validator"},
				{"type":"text","text":"answer"}
			]}]}`),
			wantFlag: true,
			wantDrop: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, report := SanitizeClaudeMessagesForClaudeUpstream(tc.input, "claude-sonnet-4-5")
			if report.DroppedForeignProviderThinking != tc.wantFlag {
				t.Fatalf("DroppedForeignProviderThinking = %v, want %v; report=%+v", report.DroppedForeignProviderThinking, tc.wantFlag, report)
			}
			if report.DroppedBlocks != tc.wantDrop {
				t.Fatalf("DroppedBlocks = %d, want %d; report=%+v", report.DroppedBlocks, tc.wantDrop, report)
			}
			if report.Preserved != tc.wantKeep {
				t.Fatalf("Preserved = %d, want %d; report=%+v", report.Preserved, tc.wantKeep, report)
			}
		})
	}
}

func TestSanitizeClaudeMessagesForClaudeUpstream_DroppedForeignDoesNotMutateThinkingConfig(t *testing.T) {
	input := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"foreign reasoning"},{"type":"text","text":"answer"}]}]}`)
	output, report := SanitizeClaudeMessagesForClaudeUpstream(input, "claude-sonnet-4-5")
	if !report.DroppedForeignProviderThinking {
		t.Fatalf("expected DroppedForeignProviderThinking; report=%+v", report)
	}
	if got := gjson.GetBytes(output, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("signature sanitizer must not rewrite thinking.type, got %q", got)
	}
	if got := gjson.GetBytes(output, "output_config.effort").String(); got != "high" {
		t.Fatalf("signature sanitizer must not rewrite output_config.effort, got %q", got)
	}
}
