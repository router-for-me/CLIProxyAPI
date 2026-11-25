package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wsrelay"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAIStudioStreamCloseWithoutStreamEndFinalizesZeroOutputGuard(t *testing.T) {
	t.Run("zero_output_candidate_shell_fails", func(t *testing.T) {
		exec := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
		req := cliproxyexecutor.Request{
			Model:   "gemini-2.5-pro",
			Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		}
		opts := cliproxyexecutor.Options{
			Stream:       true,
			SourceFormat: sdktranslator.FormatGemini,
		}
		body := translatedPayload{
			toFormat: sdktranslator.FormatGemini,
			payload:  req.Payload,
		}
		reporter := helps.NewUsageReporter(t.Context(), "aistudio", req.Model, &cliproxyauth.Auth{ID: "test-auth", Provider: "aistudio"})

		wsStream := make(chan wsrelay.StreamEvent, 2)
		wsStream <- wsrelay.StreamEvent{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: []byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\"}}],\"usageMetadata\":{\"promptTokenCount\":10}}\n\n"),
		}
		close(wsStream)

		firstEvent := wsrelay.StreamEvent{
			Type:   wsrelay.MessageTypeStreamStart,
			Status: http.StatusOK,
		}
		chunks := exec.consumeStream(t.Context(), func() {}, req, opts, body, req.Payload, reporter, nil, firstEvent, wsStream)

		var gotErr error
		for chunk := range chunks {
			if chunk.Err != nil {
				gotErr = chunk.Err
			}
		}
		if gotErr == nil {
			t.Fatal("expected zero-output error when wsStream closes without StreamEnd, got nil")
		}
		if sc, ok := gotErr.(cliproxyexecutor.StatusError); !ok || sc.StatusCode() != http.StatusBadGateway {
			t.Fatalf("expected 502 StatusError, got %T (%v)", gotErr, gotErr)
		}
	})

	t.Run("non_candidate_shell_flushed_on_eof", func(t *testing.T) {
		exec := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
		req := cliproxyexecutor.Request{
			Model:   "gemini-2.5-pro",
			Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		}
		opts := cliproxyexecutor.Options{
			Stream:       true,
			SourceFormat: sdktranslator.FormatGemini,
		}
		body := translatedPayload{
			toFormat: sdktranslator.FormatGemini,
			payload:  req.Payload,
		}
		reporter := helps.NewUsageReporter(t.Context(), "aistudio", req.Model, &cliproxyauth.Auth{ID: "test-auth", Provider: "aistudio"})

		wsStream := make(chan wsrelay.StreamEvent, 2)
		wsStream <- wsrelay.StreamEvent{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: []byte("data: {\"responseId\":\"resp-eof-shell\"}\n\n"),
		}
		close(wsStream)

		firstEvent := wsrelay.StreamEvent{
			Type:   wsrelay.MessageTypeStreamStart,
			Status: http.StatusOK,
		}
		chunks := exec.consumeStream(t.Context(), func() {}, req, opts, body, req.Payload, reporter, nil, firstEvent, wsStream)

		var payloads []string
		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream error: %v", chunk.Err)
			}
			if len(chunk.Payload) > 0 {
				payloads = append(payloads, string(chunk.Payload))
			}
		}
		joined := strings.Join(payloads, "\n")
		if !strings.Contains(joined, "resp-eof-shell") {
			t.Fatalf("expected buffered non-candidate shell to be flushed on EOF, got payloads=%q", joined)
		}
	})
}
