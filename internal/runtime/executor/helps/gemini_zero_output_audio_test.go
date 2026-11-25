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
	guard := NewGeminiZeroOutputStreamGuard(false, sdktranslator.FormatGemini)
	frame := []byte(`data: {"candidates":[{"content":{"parts":[{"audio_transcription":{"text":"hello"}}]}}],"usageMetadata":{"promptTokenCount":10}}`)
	_, action, _ := guard.Observe(frame)
	if action != GeminiZeroOutputEmit {
		t.Fatalf("Observe action = %v, want GeminiZeroOutputEmit (%v)", action, GeminiZeroOutputEmit)
	}
	if !guard.HasVisibleOutput() {
		t.Fatalf("guard.HasVisibleOutput() = false, want true")
	}
}
