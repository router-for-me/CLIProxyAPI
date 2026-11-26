package helps

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestInspectGeminiZeroOutputPayload_AudioTranscription(t *testing.T) {
	tests := []struct {
		name                 string
		payload              string
		allowResponseWrapper bool
		wantVisible          bool
		wantThought          bool
	}{
		{
			name:        "camelCase audioTranscription counts as visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"hello world"}}]},"finishReason":"STOP"}]}`,
			wantVisible: true,
		},
		{
			name:        "snake_case audio_transcription counts as visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audio_transcription":{"text":"hello world"}}]},"finishReason":"STOP"}]}`,
			wantVisible: true,
		},
		{
			name:        "audioTranscription alongside empty text part counts as visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"text":""},{"audioTranscription":{"text":"hello world"}}]},"finishReason":"STOP"}]}`,
			wantVisible: true,
		},
		{
			name:                 "wrapped response camelCase audioTranscription counts as visible output",
			payload:              `{"response":{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"transcribed text"}}]},"finishReason":"MAX_TOKENS"}]}}`,
			allowResponseWrapper: true,
			wantVisible:          true,
		},
		{
			name:        "whitespace-only audioTranscription is not visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"   "}}]},"finishReason":"STOP"}]}`,
			wantVisible: false,
		},
		{
			name:        "empty audio_transcription object is not visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audio_transcription":{}}]},"finishReason":"STOP"}]}`,
			wantVisible: false,
		},
		{
			name:        "non-string audioTranscription text is not visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":{}}}]},"finishReason":"STOP"}]}`,
			wantVisible: false,
		},
		{
			name:        "non-object audioTranscription is not visible output",
			payload:     `{"candidates":[{"content":{"parts":[{"audioTranscription":"hello"}]},"finishReason":"STOP"}]}`,
			wantVisible: false,
		},
		{
			name:        "thought audioTranscription counts as thought output only",
			payload:     `{"candidates":[{"content":{"parts":[{"thought":true,"audioTranscription":{"text":"thinking"}}]}}]}`,
			wantVisible: false,
			wantThought: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := InspectGeminiZeroOutputPayload([]byte(tc.payload), tc.allowResponseWrapper)
			if got.HasVisibleOutput != tc.wantVisible {
				t.Fatalf("HasVisibleOutput = %v, want %v", got.HasVisibleOutput, tc.wantVisible)
			}
			if got.HasThoughtOutput != tc.wantThought {
				t.Fatalf("HasThoughtOutput = %v, want %v", got.HasThoughtOutput, tc.wantThought)
			}
		})
	}
}

func TestCheckGeminiZeroOutputNonStream_AudioTranscription(t *testing.T) {
	payload := []byte(`{"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"transcribed audio"}}]},"finishReason":"MALFORMED_RESPONSE"}]}`)
	if finishReason, errMsg, zeroOutput := CheckGeminiZeroOutputNonStream(sdktranslator.FormatGemini, payload, false); zeroOutput {
		t.Fatalf("CheckGeminiZeroOutputNonStream returned zeroOutput=true (finishReason=%q, errMsg=%q), want false", finishReason, errMsg)
	}
}

func TestGeminiZeroOutputStreamGuard_AudioTranscriptionEmits(t *testing.T) {
	t.Run("emits audio_transcription frame immediately", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(false, sdktranslator.FormatGemini)
		frame := []byte(`data: {"candidates":[{"content":{"parts":[{"audio_transcription":{"text":"hello"}}]}}],"usageMetadata":{"promptTokenCount":10}}`)
		_, action, _ := guard.Observe(frame)
		if action != GeminiZeroOutputEmit {
			t.Fatalf("Observe action = %v, want GeminiZeroOutputEmit (%v)", action, GeminiZeroOutputEmit)
		}
		if !guard.HasVisibleOutput() {
			t.Fatalf("guard.HasVisibleOutput() = false, want true")
		}
	})

	t.Run("flushes leading shell when audioTranscription arrives and does not fail on EOF", func(t *testing.T) {
		guard := NewGeminiZeroOutputStreamGuard(false, sdktranslator.FormatGemini)
		shell := []byte(`data: {"candidates":[{"content":{"parts":[{"text":""}]}}]}`)
		if flush, action, _ := guard.Observe(shell); action != GeminiZeroOutputContinue || len(flush) != 0 {
			t.Fatalf("shell observe = (%d, %v), want (0, Continue)", len(flush), action)
		}
		transcript := []byte(`data: {"candidates":[{"content":{"parts":[{"audioTranscription":{"text":"hello world"}}]}}],"usageMetadata":{"promptTokenCount":10}}`)
		flush, action, _ := guard.Observe(transcript)
		if action != GeminiZeroOutputEmit || len(flush) != 1 {
			t.Fatalf("transcript observe = (%d, %v), want (1, Emit)", len(flush), action)
		}
		guard.MarkEmitted()
		if finFlush, finAction, _ := guard.Finalize(); finAction != GeminiZeroOutputEmit || len(finFlush) != 0 {
			t.Fatalf("Finalize() = (%d, %v), want (0, Emit)", len(finFlush), finAction)
		}
	})
}
