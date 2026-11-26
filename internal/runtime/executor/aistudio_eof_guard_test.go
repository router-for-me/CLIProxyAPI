package executor

import (
	"context"
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
			Payload: []byte(`data: {"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":10}}` + "\n\n"),
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

	t.Run("unterminated_zero_output_candidate_shell_fails", func(t *testing.T) {
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
			Payload: []byte(`data: {"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":10}}`),
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
			t.Fatal("expected zero-output error for unterminated candidate shell when wsStream closes without StreamEnd, got nil")
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
			Payload: []byte(`data: {"responseId":"resp-eof-shell"}` + "\n\n"),
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

	t.Run("unterminated_non_candidate_shell_flushed_on_eof", func(t *testing.T) {
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
			Payload: []byte(`data: {"responseId":"resp-eof-unterminated"}`),
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
		if !strings.Contains(joined, "resp-eof-unterminated") {
			t.Fatalf("expected buffered unterminated non-candidate shell to be flushed on EOF, got payloads=%q", joined)
		}
	})

	t.Run("non_candidate_shell_flushed_without_error_across_translated_formats", func(t *testing.T) {
		tests := []struct {
			name           string
			responseFormat sdktranslator.Format
			reqPayload     []byte
		}{
			{
				name:           "claude",
				responseFormat: sdktranslator.FormatClaude,
				reqPayload:     []byte(`{"model":"gemini-2.5-pro","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
			},
			{
				name:           "openai",
				responseFormat: sdktranslator.FormatOpenAI,
				reqPayload:     []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`),
			},
			{
				name:           "openai_response",
				responseFormat: sdktranslator.FormatOpenAIResponse,
				reqPayload:     []byte(`{"model":"gemini-2.5-pro","input":"hi","stream":true}`),
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				exec := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
				auth := &cliproxyauth.Auth{ID: "aistudio-eof-shell-" + tc.name, Provider: "aistudio"}
				req := cliproxyexecutor.Request{
					Model:   "gemini-2.5-pro",
					Payload: tc.reqPayload,
				}
				opts := cliproxyexecutor.Options{
					SourceFormat:   tc.responseFormat,
					ResponseFormat: tc.responseFormat,
					Stream:         true,
				}
				translatedReq, body, errTranslate := exec.translateRequest(context.Background(), req, opts, true)
				if errTranslate != nil {
					t.Fatalf("translateRequest() error = %v", errTranslate)
				}

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				reporter := helps.NewExecutorUsageReporter(ctx, exec, req.Model, auth)
				wsStream := make(chan wsrelay.StreamEvent, 1)
				wsStream <- wsrelay.StreamEvent{
					Type:    wsrelay.MessageTypeStreamChunk,
					Payload: []byte(`data: {"responseId":"resp-eof-shell"}` + "\n\n"),
				}
				close(wsStream)

				firstEvent := wsrelay.StreamEvent{
					Type:   wsrelay.MessageTypeStreamStart,
					Status: http.StatusOK,
				}
				chunks := exec.consumeStream(ctx, cancel, req, opts, body, translatedReq, reporter, nil, firstEvent, wsStream)

				for chunk := range chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected stream error for non-candidate shell in %s format: %v", tc.name, chunk.Err)
					}
				}
			})
		}
	})

	t.Run("claude_post_commit_abnormal_finish_emits_error_then_fails", func(t *testing.T) {
		exec := NewAIStudioExecutor(&config.Config{}, "aistudio", nil)
		auth := &cliproxyauth.Auth{ID: "aistudio-eof-claude-malformed", Provider: "aistudio"}
		req := cliproxyexecutor.Request{
			Model:   "gemini-2.5-pro",
			Payload: []byte(`{"model":"gemini-2.5-pro","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`),
		}
		opts := cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
			Stream:         true,
		}
		translatedReq, body, errTranslate := exec.translateRequest(context.Background(), req, opts, true)
		if errTranslate != nil {
			t.Fatalf("translateRequest() error = %v", errTranslate)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reporter := helps.NewExecutorUsageReporter(ctx, exec, req.Model, auth)
		wsStream := make(chan wsrelay.StreamEvent, 2)
		wsStream <- wsrelay.StreamEvent{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"planning"}]}}],"modelVersion":"gemini-2.5-pro"}` + "\n\n"),
		}
		wsStream <- wsrelay.StreamEvent{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: []byte(`data: {"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL"}],"modelVersion":"gemini-2.5-pro"}` + "\n\n"),
		}
		close(wsStream)

		firstEvent := wsrelay.StreamEvent{
			Type:   wsrelay.MessageTypeStreamStart,
			Status: http.StatusOK,
		}
		chunks := exec.consumeStream(ctx, cancel, req, opts, body, translatedReq, reporter, nil, firstEvent, wsStream)

		var payloads []string
		var gotErr error
		for chunk := range chunks {
			if chunk.Err != nil {
				gotErr = chunk.Err
			}
			if len(chunk.Payload) > 0 {
				payloads = append(payloads, string(chunk.Payload))
			}
		}
		if !strings.Contains(strings.Join(payloads, "\n"), "event: error") {
			t.Fatalf("expected Claude error SSE event before 502 failure, got payloads=%q", strings.Join(payloads, "\n"))
		}
		if gotErr == nil {
			t.Fatal("expected 502 error on post-commit abnormal finish EOF, got nil")
		}
		if sc, ok := gotErr.(cliproxyexecutor.StatusError); !ok || sc.StatusCode() != http.StatusBadGateway {
			t.Fatalf("expected 502 StatusError, got %T (%v)", gotErr, gotErr)
		}
	})

	t.Run("canceled_context_skips_eof_finalization", func(t *testing.T) {
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
		ctx, cancel := context.WithCancel(t.Context())
		reporter := helps.NewUsageReporter(ctx, "aistudio", req.Model, &cliproxyauth.Auth{ID: "test-auth", Provider: "aistudio"})

		wsStream := make(chan wsrelay.StreamEvent, 2)
		wsStream <- wsrelay.StreamEvent{
			Type:    wsrelay.MessageTypeStreamChunk,
			Payload: []byte(`data: {"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":10}}` + "\n\n"),
		}
		cancel()
		close(wsStream)

		firstEvent := wsrelay.StreamEvent{
			Type:   wsrelay.MessageTypeStreamStart,
			Status: http.StatusOK,
		}
		chunks := exec.consumeStream(ctx, func() {}, req, opts, body, req.Payload, reporter, nil, firstEvent, wsStream)

		for chunk := range chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected error when context is already canceled: %v", chunk.Err)
			}
		}
	})
}
