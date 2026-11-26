package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type interactionsStreamRoundTripFunc func(*http.Request) (*http.Response, error)

func (f interactionsStreamRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type scriptedStreamBody struct {
	chunks      [][]byte
	finalErr    error
	onFinalRead func()
	index       int
	done        bool
}

func (b *scriptedStreamBody) Read(p []byte) (int, error) {
	if b.index < len(b.chunks) {
		n := copy(p, b.chunks[b.index])
		if n < len(b.chunks[b.index]) {
			b.chunks[b.index] = b.chunks[b.index][n:]
		} else {
			b.index++
		}
		return n, nil
	}
	if !b.done {
		b.done = true
		if b.onFinalRead != nil {
			b.onFinalRead()
		}
	}
	if b.finalErr != nil {
		return 0, b.finalErr
	}
	return 0, io.EOF
}

func (b *scriptedStreamBody) Close() error {
	return nil
}

func newInteractionsStreamResponse(body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body: body,
	}
}

func newGeminiInteractionsTestExecutorAndAuth() (interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, *cliproxyauth.Auth) {
	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "gemini-interactions-test",
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": "https://example.invalid",
		},
	}
	return exec, auth
}

func newGeminiVertexInteractionsTestExecutorAndAuth() (interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, *cliproxyauth.Auth) {
	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "vertex-interactions-test",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     "https://example.invalid",
			"interactions": "true",
		},
	}
	return exec, auth
}

func TestGeminiInteractionsScannerErrDoesNotFlushTruncatedFrame(t *testing.T) {
	exec, auth := newGeminiInteractionsTestExecutorAndAuth()
	assertInteractionsScannerErrDoesNotFlushTruncatedFrame(t, exec, auth)
}

func TestGeminiVertexInteractionsScannerErrDoesNotFlushTruncatedFrame(t *testing.T) {
	exec, auth := newGeminiVertexInteractionsTestExecutorAndAuth()
	assertInteractionsScannerErrDoesNotFlushTruncatedFrame(t, exec, auth)
}

func assertInteractionsScannerErrDoesNotFlushTruncatedFrame(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, auth *cliproxyauth.Auth) {
	t.Helper()

	errStreamReset := errors.New("upstream connection reset mid-frame")
	const completeFrame = "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\n"
	const truncatedFrame = "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"delta\":{\"text\":\"truncated"

	for _, tc := range []struct {
		name           string
		responseFormat sdktranslator.Format
	}{
		{name: "passthrough_interactions", responseFormat: sdktranslator.FormatInteractions},
		{name: "translated_claude", responseFormat: sdktranslator.FormatClaude},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", interactionsStreamRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newInteractionsStreamResponse(&scriptedStreamBody{
					chunks:   [][]byte{[]byte(completeFrame + truncatedFrame)},
					finalErr: errStreamReset,
				}), nil
			}))

			result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-3.5-flash",
				Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi","stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatInteractions,
				ResponseFormat: tc.responseFormat,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var payloads [][]byte
			var gotErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					gotErr = chunk.Err
					continue
				}
				payloads = append(payloads, bytes.Clone(chunk.Payload))
			}

			if !errors.Is(gotErr, errStreamReset) {
				t.Fatalf("stream error = %v, want %v", gotErr, errStreamReset)
			}
			for _, payload := range payloads {
				if bytes.Contains(payload, []byte("truncated")) || bytes.Contains(payload, []byte("step.delta")) {
					t.Fatalf("truncated SSE frame was flushed downstream despite scanner error: %q", string(payload))
				}
			}
			if len(payloads) == 0 {
				t.Fatal("expected the complete frame before truncation to be emitted")
			}
		})
	}
}

func TestGeminiInteractionsScannerErrRecordedWhenContextCanceled(t *testing.T) {
	exec, auth := newGeminiInteractionsTestExecutorAndAuth()
	assertInteractionsScannerErrRecordedWhenContextCanceled(t, exec, auth)
}

func TestGeminiVertexInteractionsScannerErrRecordedWhenContextCanceled(t *testing.T) {
	exec, auth := newGeminiVertexInteractionsTestExecutorAndAuth()
	assertInteractionsScannerErrRecordedWhenContextCanceled(t, exec, auth)
}

func assertInteractionsScannerErrRecordedWhenContextCanceled(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, auth *cliproxyauth.Auth) {
	t.Helper()

	errStreamReset := errors.New("upstream reset while context canceled")
	const completeFrame = "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\n"
	const truncatedFrame = "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"delta\":{\"text\":\"truncated"

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	capture := logging.NewDeferredAPICapture()
	ctx := logging.WithDeferredAPICapture(baseCtx, capture)

	finalReadStarted := make(chan struct{})
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", interactionsStreamRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newInteractionsStreamResponse(&scriptedStreamBody{
			chunks:   [][]byte{[]byte(completeFrame + truncatedFrame)},
			finalErr: errStreamReset,
			onFinalRead: func() {
				cancel()
				close(finalReadStarted)
			},
		}), nil
	}))

	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash",
		Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	firstChunk, ok := <-result.Chunks
	if !ok || firstChunk.Err != nil {
		t.Fatalf("first chunk = (%v, ok=%t), want complete frame without error", firstChunk, ok)
	}

	// Wait until the stream body has canceled ctx and returned the scanner error
	// while no receiver is reading from result.Chunks.
	<-finalReadStarted

	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 && bytes.Contains(chunk.Payload, []byte("truncated")) {
			t.Fatalf("truncated SSE frame was flushed downstream on canceled context: %q", string(chunk.Payload))
		}
	}

	diagnostics := capture.ResponseDiagnostics()
	found := false
	for _, diag := range diagnostics {
		if diag.Error != nil && diag.Error.Error() == errStreamReset.Error() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("scanner error %q was not recorded in API response diagnostics: %+v", errStreamReset, diagnostics)
	}
}

func TestGeminiInteractionsCleanEOFFlushesTrailingFrame(t *testing.T) {
	exec, auth := newGeminiInteractionsTestExecutorAndAuth()
	assertInteractionsCleanEOFFlushesTrailingFrame(t, exec, auth)
}

func TestGeminiVertexInteractionsCleanEOFFlushesTrailingFrame(t *testing.T) {
	exec, auth := newGeminiVertexInteractionsTestExecutorAndAuth()
	assertInteractionsCleanEOFFlushesTrailingFrame(t, exec, auth)
}

func assertInteractionsCleanEOFFlushesTrailingFrame(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, auth *cliproxyauth.Auth) {
	t.Helper()

	const trailingOnlyFrame = "event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"completed\"}}"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", interactionsStreamRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newInteractionsStreamResponse(&scriptedStreamBody{
			chunks: [][]byte{[]byte(trailingOnlyFrame)},
		}), nil
	}))

	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash",
		Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var payloads [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payloads = append(payloads, bytes.Clone(chunk.Payload))
	}
	if len(payloads) != 1 || !bytes.Contains(payloads[0], []byte("interaction.completed")) {
		t.Fatalf("payloads = %q, want trailing frame flushed on clean EOF", payloads)
	}
}

func TestGeminiInteractionsApplyPatchScannerErrRecordsDiagnosticAndEmitsSanitizedFailure(t *testing.T) {
	exec, auth := newGeminiInteractionsTestExecutorAndAuth()
	assertInteractionsApplyPatchScannerErrRecordsDiagnosticAndEmitsSanitizedFailure(t, exec, auth)
}

func TestGeminiVertexInteractionsApplyPatchScannerErrRecordsDiagnosticAndEmitsSanitizedFailure(t *testing.T) {
	exec, auth := newGeminiVertexInteractionsTestExecutorAndAuth()
	assertInteractionsApplyPatchScannerErrRecordsDiagnosticAndEmitsSanitizedFailure(t, exec, auth)
}

func assertInteractionsApplyPatchScannerErrRecordsDiagnosticAndEmitsSanitizedFailure(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, auth *cliproxyauth.Auth) {
	t.Helper()

	errStreamReset := errors.New("upstream connection reset during apply_patch")
	sseBody := task6ProviderFixture("gemini-interactions", "eof", "apply_patch")

	capture := logging.NewDeferredAPICapture()
	ctx := logging.WithDeferredAPICapture(context.Background(), capture)
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", interactionsStreamRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newInteractionsStreamResponse(&scriptedStreamBody{
			chunks:   [][]byte{[]byte(sseBody)},
			finalErr: errStreamReset,
		}), nil
	}))

	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash",
		Payload: []byte(`{"model":"gemini-3.5-flash","input":"patch","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatInteractions,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: []byte(task6PatchRequest),
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	assertTask6FailedStream(t, result.Chunks)

	diagnostics := capture.ResponseDiagnostics()
	found := false
	for _, diag := range diagnostics {
		if diag.Error != nil && diag.Error.Error() == errStreamReset.Error() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("scanner error %q was not recorded in API response diagnostics when EndApplyPatchStream returned early: %+v", errStreamReset, diagnostics)
	}
}

func TestGeminiInteractionsUnterminatedWhitespaceTailDoesNotFlushOnScannerErr(t *testing.T) {
	exec, auth := newGeminiInteractionsTestExecutorAndAuth()
	assertInteractionsUnterminatedWhitespaceTailDoesNotFlushOnScannerErr(t, exec, auth)
}

func TestGeminiVertexInteractionsUnterminatedWhitespaceTailDoesNotFlushOnScannerErr(t *testing.T) {
	exec, auth := newGeminiVertexInteractionsTestExecutorAndAuth()
	assertInteractionsUnterminatedWhitespaceTailDoesNotFlushOnScannerErr(t, exec, auth)
}

func assertInteractionsUnterminatedWhitespaceTailDoesNotFlushOnScannerErr(t *testing.T, exec interface {
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}, auth *cliproxyauth.Auth) {
	t.Helper()

	errStreamReset := errors.New("upstream reset on unterminated whitespace line")
	const completeFrame = "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\n"
	const partialFrame = "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"delta\":{\"text\":\"unterminated_ws\"}}\n"

	for _, tc := range []struct {
		name string
		tail string
	}{
		{name: "space_without_newline", tail: " "},
		{name: "cr_without_newline", tail: "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", interactionsStreamRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return newInteractionsStreamResponse(&scriptedStreamBody{
					chunks:   [][]byte{[]byte(completeFrame + partialFrame + tc.tail)},
					finalErr: errStreamReset,
				}), nil
			}))

			result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-3.5-flash",
				Payload: []byte(`{"model":"gemini-3.5-flash","input":"hi","stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatInteractions,
				ResponseFormat: sdktranslator.FormatInteractions,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var payloads [][]byte
			var gotErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					gotErr = chunk.Err
					continue
				}
				payloads = append(payloads, bytes.Clone(chunk.Payload))
			}

			if !errors.Is(gotErr, errStreamReset) {
				t.Fatalf("stream error = %v, want %v", gotErr, errStreamReset)
			}
			if len(payloads) != 1 || !bytes.Contains(payloads[0], []byte("interaction.created")) {
				t.Fatalf("payloads = %q, want only the complete frame before the unterminated whitespace tail", payloads)
			}
			for _, payload := range payloads {
				if bytes.Contains(payload, []byte("unterminated_ws")) || bytes.Contains(payload, []byte("step.delta")) {
					t.Fatalf("unterminated SSE frame was flushed by unterminated whitespace tail: %q", string(payload))
				}
			}
		})
	}
}
