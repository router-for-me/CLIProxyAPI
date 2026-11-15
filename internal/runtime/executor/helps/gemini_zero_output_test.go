package helps

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestInspectGeminiZeroOutputPayload(t *testing.T) {
	tests := []struct {
		name                 string
		payload              string
		allowResponseWrapper bool
		wantReason           string
		wantVisible          bool
		wantThought          bool
		wantUsage            bool
	}{
		{
			name:                 "antigravity partless malformed",
			payload:              `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":10}}}`,
			allowResponseWrapper: true,
			wantReason:           "MALFORMED_FUNCTION_CALL",
			wantUsage:            true,
		},
		{
			name:        "gemini whitespace and empty parts unexpected tool call",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"   "},{"thought":true,"text":"thinking"}]},"finishReason":"UNEXPECTED_TOOL_CALL"}]}`,
			wantReason:  "UNEXPECTED_TOOL_CALL",
			wantThought: true,
		},
		{
			name:        "gemini valid functionCall with malformed finish reason keeps visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{}}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantReason:  "MALFORMED_FUNCTION_CALL",
			wantVisible: true,
		},
		{
			name:        "gemini inlineData media keeps visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"MALFORMED_RESPONSE"}]}`,
			wantReason:  "MALFORMED_RESPONSE",
			wantVisible: true,
		},
		{
			name:                 "cpaUsageMetadata does not count as upstream usageMetadata",
			payload:              `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"cpaUsageMetadata":{"promptTokenCount":12}}}`,
			allowResponseWrapper: true,
			wantUsage:            false,
		},
		{
			name:        "empty functionCall placeholder is not visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantReason:  "MALFORMED_FUNCTION_CALL",
			wantVisible: false,
		},
		{
			name:        "empty inlineData and fileData placeholders are not visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":""}},{"fileData":{}},{"executableCode":{}},{"codeExecutionResult":{}}]},"finishReason":"MALFORMED_RESPONSE"}]}`,
			wantReason:  "MALFORMED_RESPONSE",
			wantVisible: false,
		},
		{
			name:        "vertex streaming partialArgs functionCall counts as visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"partialArgs":[{"jsonPath":"$.cmd","stringValue":"ls"}]}}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantReason:  "MALFORMED_FUNCTION_CALL",
			wantVisible: true,
		},
		{
			name:        "partial non-whitespace text counts as visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial answer"}]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
			wantReason:  "MALFORMED_FUNCTION_CALL",
			wantVisible: true,
		},
		{
			name:        "valid fileData executableCode and codeExecutionResult keep visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"code":"print(1)"}}]},"finishReason":"UNEXPECTED_TOOL_CALL"}]}`,
			wantReason:  "UNEXPECTED_TOOL_CALL",
			wantVisible: true,
		},
		{
			name:        "thought inlineData counts as thought only, not visible output",
			payload:     `{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"inlineData":{"mimeType":"image/png","data":"AA=="}}]},"finishReason":"MALFORMED_RESPONSE"}]}`,
			wantReason:  "MALFORMED_RESPONSE",
			wantVisible: false,
			wantThought: true,
		},
		{
			name:                 "antigravity non-thought thoughtSignature with empty text does not count as thought or visible output",
			payload:              `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"opaque-sig","text":""}]}}],"usageMetadata":{"promptTokenCount":378515,"cachedContentTokenCount":376466,"totalTokenCount":378515}}}`,
			allowResponseWrapper: true,
			wantVisible:          false,
			wantThought:          false,
			wantUsage:            true,
		},
		{
			name:                 "antigravity thought flag with thoughtSignature and empty text does not count as thought or visible output",
			payload:              `{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"thoughtSignature":"opaque-sig","text":""}]}}]}}`,
			allowResponseWrapper: true,
			wantVisible:          false,
			wantThought:          false,
			wantUsage:            false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InspectGeminiZeroOutputPayload([]byte(tc.payload), tc.allowResponseWrapper)
			if got.FinishReason != tc.wantReason || got.HasVisibleOutput != tc.wantVisible || got.HasThoughtOutput != tc.wantThought || got.HasUsageMetadata != tc.wantUsage {
				t.Fatalf("InspectGeminiZeroOutputPayload() = %+v, want reason=%q visible=%v thought=%v usage=%v",
					got, tc.wantReason, tc.wantVisible, tc.wantThought, tc.wantUsage)
			}
		})
	}
}

func TestGeminiZeroOutputStreamGuard_ShellBufferingAndSplitUsage(t *testing.T) {
	t.Run("buffers leading shells then flushes when real content arrives", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(true, sdktranslator.FormatGemini)
		shell := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]}}]}}`)
		if flush, action, _ := guard.Observe(shell); action != GeminiZeroOutputContinue || len(flush) != 0 {
			t.Fatalf("shell observe = (%d, %v), want (0, Continue)", len(flush), action)
		}
		textFrame := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}]}}`)
		flush, action, _ := guard.Observe(textFrame)
		if action != GeminiZeroOutputEmit || len(flush) != 1 {
			t.Fatalf("content observe = (%d, %v), want (1, Emit)", len(flush), action)
		}
		guard.MarkEmitted()
		if !guard.Emitted() {
			t.Fatal("expected Emitted() = true after MarkEmitted()")
		}
		if !guard.HasVisibleOutput() {
			t.Fatal("expected HasVisibleOutput() = true after real text chunk")
		}
		if guard.HasAbnormalFinish() {
			t.Fatal("expected HasAbnormalFinish() = false for normal text chunk")
		}
		if finFlush, finAction, _ := guard.Finalize(); finAction != GeminiZeroOutputEmit || len(finFlush) != 0 {
			t.Fatalf("Finalize() = (%d, %v), want (0, Emit)", len(finFlush), finAction)
		}
	})

	t.Run("discards leading shells and waits for trailing usage on zero-output abnormal finish", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(false, sdktranslator.FormatOpenAI)
		shell := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}]}`)
		if _, action, _ := guard.Observe(shell); action != GeminiZeroOutputContinue {
			t.Fatalf("shell action = %v, want Continue", action)
		}
		abnormal := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_RESPONSE"}]}`)
		if flush, action, reason := guard.Observe(abnormal); action != GeminiZeroOutputContinue || len(flush) != 0 || reason != "MALFORMED_RESPONSE" {
			t.Fatalf("abnormal without usage = (%d, %v, %q), want (0, Continue, MALFORMED_RESPONSE)", len(flush), action, reason)
		}
		usageOnly := []byte(`{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"totalTokenCount":130}}`)
		if flush, action, reason := guard.Observe(usageOnly); action != GeminiZeroOutputFailNow || len(flush) != 0 || reason != "MALFORMED_RESPONSE" {
			t.Fatalf("usage after abnormal = (%d, %v, %q), want (0, FailNow, MALFORMED_RESPONSE)", len(flush), action, reason)
		}
		if guard.Emitted() {
			t.Fatal("expected Emitted() = false when pre-bootstrap zero-output fails")
		}
	})

	t.Run("thought chunk is unbuffered and post-thought malformed claude emits error then fails", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(false, sdktranslator.FormatClaude)
		thought := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"hmm"}]}}]}`)
		flush, action, _ := guard.Observe(thought)
		if action != GeminiZeroOutputEmit || len(flush) != 0 {
			t.Fatalf("thought chunk must emit immediately without buffering: (%d, %v)", len(flush), action)
		}
		guard.MarkEmitted()
		abnormalNoUsage := []byte(`{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}`)
		if _, action, _ := guard.Observe(abnormalNoUsage); action != GeminiZeroOutputContinue {
			t.Fatalf("post-thought abnormal without usage = %v, want Continue awaiting usage", action)
		}
		if !guard.HasAbnormalFinish() {
			t.Fatal("expected HasAbnormalFinish() = true while awaiting trailing usage")
		}
		if guard.HasVisibleOutput() {
			t.Fatal("expected HasVisibleOutput() = false after thought-only chunk")
		}
		usageOnly := []byte(`{"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}`)
		_, action, reason := guard.Observe(usageOnly)
		if action != GeminiZeroOutputEmitErrorThenFail || reason != "MALFORMED_FUNCTION_CALL" {
			t.Fatalf("post-thought claude usage action = (%v, %q), want (EmitErrorThenFail, MALFORMED_FUNCTION_CALL)", action, reason)
		}
		if len(guard.AbnormalFrame()) == 0 {
			t.Fatal("expected retained abnormal frame for split post-thought Claude error emission")
		}
	})

	t.Run("preliminary usage on early frame does not short-circuit final usage after abnormal finish", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(true, sdktranslator.FormatGemini)
		earlyShellWithPrelimUsage := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"promptTokenCount":120,"totalTokenCount":120}}}`)
		if _, action, _ := guard.Observe(earlyShellWithPrelimUsage); action != GeminiZeroOutputContinue {
			t.Fatalf("early shell action = %v, want Continue", action)
		}
		abnormalNoUsage := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`)
		if _, action, _ := guard.Observe(abnormalNoUsage); action != GeminiZeroOutputContinue {
			t.Fatalf("abnormal without usage after preliminary usage = %v, want Continue awaiting final usage", action)
		}
		finalUsage := []byte(`{"response":{"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":10,"thoughtsTokenCount":313,"totalTokenCount":443}}}`)
		if _, action, reason := guard.Observe(finalUsage); action != GeminiZeroOutputFailNow || reason != "MALFORMED_FUNCTION_CALL" {
			t.Fatalf("final usage observe = (%v, %q), want (FailNow, MALFORMED_FUNCTION_CALL)", action, reason)
		}
	})

	t.Run("leading thoughtSignature-only empty-text frame buffers and fails on EOF without finishReason", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(true, sdktranslator.FormatOpenAI)
		sigOnly := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"opaque-sig","text":""}]}}],"usageMetadata":{"promptTokenCount":378515,"totalTokenCount":378515},"modelVersion":"gemini-4-hydrogen","responseId":"resp-7536"}}`)
		if flush, action, _ := guard.Observe(sigOnly); action != GeminiZeroOutputContinue || len(flush) != 0 {
			t.Fatalf("sig-only observe = (%d, %v), want (0, Continue)", len(flush), action)
		}
		if finFlush, finAction, _ := guard.Finalize(); finAction != GeminiZeroOutputFailNow || len(finFlush) != 0 {
			t.Fatalf("Finalize() = (%d, %v), want (0, FailNow)", len(finFlush), finAction)
		}
	})

	t.Run("leading thoughtSignature-only empty-text frame flushes when real content follows", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(true, sdktranslator.FormatOpenAI)
		sigOnly := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"opaque-sig","text":""}]}}]}}`)
		if flush, action, _ := guard.Observe(sigOnly); action != GeminiZeroOutputContinue || len(flush) != 0 {
			t.Fatalf("sig-only observe = (%d, %v), want (0, Continue)", len(flush), action)
		}
		textFrame := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}]}}`)
		flush, action, _ := guard.Observe(textFrame)
		if action != GeminiZeroOutputEmit || len(flush) != 1 {
			t.Fatalf("content observe = (%d, %v), want (1, Emit)", len(flush), action)
		}
		guard.MarkEmitted()
		if finFlush, finAction, _ := guard.Finalize(); finAction != GeminiZeroOutputEmit || len(finFlush) != 0 {
			t.Fatalf("Finalize() = (%d, %v), want (0, Emit)", len(finFlush), finAction)
		}
	})
}
