package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Upstream closes the SSE stream after a partial message without ever sending
// message_stop. The executor must surface a truncation error and record failed usage
// instead of closing the channel silently.
func TestClaudeExecutorStreamTruncationReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Abrupt clean EOF: no message_stop, no error event.
	}))
	defer server.Close()

	pluginName := "test-claude-passthrough-truncation"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunkErr error
	var sawMessageStop bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
			continue
		}
		if strings.Contains(string(chunk.Payload), "message_stop") {
			sawMessageStop = true
		}
	}

	if chunkErr == nil {
		t.Fatalf("expected a truncation error chunk when upstream closes before message_stop, got none (sawMessageStop=%v)", sawMessageStop)
	}
	if !strings.Contains(chunkErr.Error(), "message_stop") {
		t.Errorf("truncation error = %q, want it to mention message_stop", chunkErr.Error())
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("expected usage record to be marked failed on truncated stream")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for failed usage record")
	}
}

// Translated stream path must also report a truncation error when upstream closes
// before message_stop.
func TestClaudeExecutorTranslatedStreamTruncationReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	pluginName := "test-claude-translated-truncation"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}

	if chunkErr == nil {
		t.Fatal("expected a truncation error chunk on translated path when upstream closes before message_stop, got none")
	}
	if !strings.Contains(chunkErr.Error(), "message_stop") {
		t.Errorf("truncation error = %q, want it to mention message_stop", chunkErr.Error())
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("expected usage record to be marked failed on truncated translated stream")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for failed usage record")
	}
}

// A complete stream that ends with message_stop must not report any error.
func TestClaudeExecutorStreamCompleteNoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected error chunk in complete stream: %v", chunk.Err)
		}
	}
}

// OpenAICompatExecutor with ResponseFormat: FormatClaude must fail the stream on clean EOF
// when no chunk carried finish_reason, rather than synthesizing [DONE] and emitting a fake message_stop.
func TestOpenAICompatExecutorClaudeEOFBeforeFinishReasonFailsStream(t *testing.T) {
	reply := "data: {\"id\":\"chatcmpl-truncated\",\"object\":\"chat.completion.chunk\",\"created\":1773896263,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"content_block_delta"`) {
		t.Fatalf("truncated Claude stream did not deliver its valid content frame: %q", output.String())
	}
	if streamErr == nil {
		t.Fatalf("expected error chunk on truncated OpenAI-to-Claude stream missing finish_reason, got nil; output=%q", output.String())
	}
	if strings.Contains(output.String(), "message_stop") {
		t.Fatalf("truncated OpenAI-to-Claude stream must not synthesize message_stop: %q", output.String())
	}
}

// OpenAICompatExecutor with ResponseFormat: FormatClaude must still finalize the stream on clean EOF
// when a chunk already carried a non-empty finish_reason (compatibility with providers omitting [DONE]).
func TestOpenAICompatExecutorClaudeEOFAfterFinishReasonCompletesStream(t *testing.T) {
	reply := strings.Join([]string{
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
	}, "\n")
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after finish_reason was received: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), "message_stop") {
		t.Fatalf("expected message_stop when finish_reason was received before clean EOF: %q", output.String())
	}
}

// GeminiExecutor with ResponseFormat: FormatClaude must fail the stream on clean EOF
// when no chunk carried a finishReason, instead of synthesizing [DONE] and emitting a fake message_stop.
func TestGeminiExecutorClaudeStreamTruncationReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial gemini\"}]}}]}\n\n")
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-gemini-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var output bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	if streamErr == nil {
		t.Fatalf("expected error chunk on truncated Gemini-to-Claude stream missing finishReason, got nil; output=%q", output.String())
	}
	if strings.Contains(output.String(), "message_stop") {
		t.Fatalf("truncated Gemini-to-Claude stream must not synthesize message_stop: %q", output.String())
	}
}

// GeminiVertexExecutor with ResponseFormat: FormatClaude must fail the stream on clean EOF
// when no chunk carried a finishReason, instead of synthesizing [DONE] and emitting a fake message_stop.
func TestGeminiVertexExecutorClaudeStreamTruncationReportsError(t *testing.T) {
	for _, authMode := range []string{"api_key", "service_account"} {
		t.Run(authMode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial vertex\"}]}}]}\n\n")
			}))
			defer server.Close()

			ctx := context.Background()
			auth := &cliproxyauth.Auth{Provider: "vertex"}
			if authMode == "api_key" {
				auth.Attributes = map[string]string{
					"api_key":  "test-vertex-key",
					"base_url": server.URL,
				}
			} else {
				var serviceAccount map[string]any
				if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
					t.Fatal(errUnmarshal)
				}
				auth.Metadata = map[string]any{
					"project_id":      "proxy-test",
					"location":        "global",
					"service_account": serviceAccount,
				}
				target, errParse := parseTestURL(t, server.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				t.Cleanup(transport.CloseIdleConnections)
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
			}

			exec := NewGeminiVertexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{
				Model:   "gemini-2.5-flash",
				Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}

			result, err := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			var output bytes.Buffer
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
					continue
				}
				output.Write(chunk.Payload)
			}
			if streamErr == nil {
				t.Fatalf("expected error chunk on truncated Vertex-to-Claude stream missing finishReason, got nil; output=%q", output.String())
			}
			if strings.Contains(output.String(), "message_stop") {
				t.Fatalf("truncated Vertex-to-Claude stream must not synthesize message_stop: %q", output.String())
			}
		})
	}
}

// Upstream SSE error event after message_start must not be treated as a completed
// message for continuity/usage accounting: claudeMessageIDFromSSE must return empty
// and ExecuteStream must surface a terminal chunk error and failed usage record.
func TestClaudeExecutorStreamErrorEventDoesNotCommitContinuityAndMarksFailed(t *testing.T) {
	sseWithError := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_err_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: error\n" +
		"data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"

	if gotID := claudeMessageIDFromSSE([]byte(sseWithError)); gotID != "" {
		t.Fatalf("claudeMessageIDFromSSE() on error stream = %q, want empty string so failed turn is not committed", gotID)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseWithError)
	}))
	defer server.Close()

	pluginName := "test-claude-error-event-usage"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunkErr error
	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}

	if !strings.Contains(output.String(), "overloaded_error") {
		t.Fatalf("expected upstream error event payload to be forwarded, got: %q", output.String())
	}
	if chunkErr == nil {
		t.Fatal("expected terminal chunk error when upstream emits SSE error event without message_stop, got nil")
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatal("expected usage record to be marked failed when upstream stream ends with error event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for failed usage record")
	}
}

type failingTailReader struct {
	prefix io.Reader
	err    error
}

func (r *failingTailReader) Read(p []byte) (int, error) {
	if r.prefix != nil {
		n, err := r.prefix.Read(p)
		if err == io.EOF {
			r.prefix = nil
			if n > 0 {
				return n, nil
			}
			return 0, r.err
		}
		return n, err
	}
	return 0, r.err
}

func (r *failingTailReader) Close() error { return nil }

// KimiExecutor.ExecuteStream must check scanner.Err() before synthesizing [DONE]
// so a mid-stream read error does not emit a synthetic terminal completion chunk first.
func TestKimiExecutorStreamReadErrorDoesNotSynthesizeDoneBeforeScanError(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &failingTailReader{
				prefix: strings.NewReader("data: {\"id\":\"chatcmpl-kimi\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"part\"},\"finish_reason\":null}]}\n\n"),
				err:    io.ErrUnexpectedEOF,
			},
		}, nil
	}))

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "test-kimi-key"},
	}
	req := cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}

	result, err := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var output bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	if streamErr == nil {
		t.Fatalf("expected scan error from KimiExecutor stream, got nil; output=%q", output.String())
	}
	if strings.Contains(output.String(), "response.completed") {
		t.Fatalf("KimiExecutor must not synthesize [DONE] (response.completed) when scanner.Err() != nil; output=%q", output.String())
	}
}

// GeminiExecutor and GeminiVertexExecutor must still complete FormatClaude streams
// when an upstream chunk carries finishReason or promptFeedback.blockReason.
func TestGeminiAndVertexClaudeStreamWithFinishSignalCompletes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chunk string
	}{
		{
			name:  "finishReason STOP",
			chunk: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1}}\n\n",
		},
		{
			name:  "promptFeedback blockReason",
			chunk: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"blocked\"}]}}],\"promptFeedback\":{\"blockReason\":\"SAFETY\"}}\n\n",
		},
	} {
		t.Run("gemini/"+tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.chunk)
			}))
			defer server.Close()

			exec := NewGeminiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "gemini",
				Attributes: map[string]string{
					"api_key":  "test-gemini-key",
					"base_url": server.URL,
				},
			}
			result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-2.5-flash",
				Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			var output bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected error chunk: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), "message_stop") {
				t.Fatalf("expected message_stop on completed Gemini stream; output=%q", output.String())
			}
		})

		t.Run("vertex/"+tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.chunk)
			}))
			defer server.Close()

			exec := NewGeminiVertexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "vertex",
				Attributes: map[string]string{
					"api_key":  "test-vertex-key",
					"base_url": server.URL,
				},
			}
			result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-2.5-flash",
				Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			var output bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected error chunk: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), "message_stop") {
				t.Fatalf("expected message_stop on completed Vertex stream; output=%q", output.String())
			}
		})

		t.Run("vertex_service_account/"+tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, tc.chunk)
			}))
			defer server.Close()

			var serviceAccount map[string]any
			if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			auth := &cliproxyauth.Auth{
				Provider: "vertex",
				Metadata: map[string]any{
					"project_id":      "proxy-test",
					"location":        "global",
					"service_account": serviceAccount,
				},
			}
			target, errParse := parseTestURL(t, server.URL)
			if errParse != nil {
				t.Fatal(errParse)
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			t.Cleanup(transport.CloseIdleConnections)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})

			exec := NewGeminiVertexExecutor(&config.Config{})
			result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "gemini-2.5-flash",
				Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}
			var output bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected error chunk: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !strings.Contains(output.String(), "message_stop") {
				t.Fatalf("expected message_stop on completed Vertex service_account stream; output=%q", output.String())
			}
		})
	}
}

func TestClaudeExecutorMultiLineDataMessageStopAndErrorEvents(t *testing.T) {
	multiLineSuccess := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\n" +
		"data: \"message\":{\"id\":\"msg_multi_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\n" +
		"data: \"message_stop\"}\n\n"

	if gotID := claudeMessageIDFromSSE([]byte(multiLineSuccess)); gotID != "msg_multi_1" {
		t.Fatalf("claudeMessageIDFromSSE() on multi-line SSE = %q, want %q", gotID, "msg_multi_1")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, multiLineSuccess)
	}))
	defer server.Close()

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected error on multi-line message_stop stream: %v", chunk.Err)
		}
	}

	nestedMultiLineTruncated := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_nested_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":\n" +
		"data: {\"type\":\"message_stop\"}\n" +
		"data: }}\n\n"

	if gotID := claudeMessageIDFromSSE([]byte(nestedMultiLineTruncated)); gotID != "" {
		t.Fatalf("claudeMessageIDFromSSE() on truncated stream with nested message_stop line = %q, want empty string", gotID)
	}

	truncServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, nestedMultiLineTruncated)
	}))
	defer truncServer.Close()

	truncResult, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": truncServer.URL,
	}}, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var nestedChunkErr error
	for chunk := range truncResult.Chunks {
		if chunk.Err != nil {
			nestedChunkErr = chunk.Err
		}
	}
	if nestedChunkErr == nil {
		t.Fatal("expected truncation error when stream ends after multiline tool_use with nested message_stop line, got nil")
	}
}

type captureAnyProviderUsagePlugin struct {
	provider string
	records  chan usage.Record
}

func (p *captureAnyProviderUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || (p.provider != "" && record.Provider != p.provider) {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

func TestTranslatedClaudeStreamTruncationPreservesBufferedUsageOnFailure(t *testing.T) {
	t.Run("gemini", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":4,\"totalTokenCount\":16}}\n\n")
		}))
		defer server.Close()

		pluginName := "test-gemini-claude-truncation-usage"
		plugin := &captureAnyProviderUsagePlugin{provider: "gemini", records: make(chan usage.Record, 4)}
		usage.RegisterNamedPlugin(pluginName, plugin)
		t.Cleanup(func() {
			usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
		})

		exec := NewGeminiExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "gemini",
			Attributes: map[string]string{
				"api_key":  "test-gemini-key",
				"base_url": server.URL,
			},
		}
		result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-2.5-flash",
			Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
			Stream:         true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for range result.Chunks {
		}

		select {
		case record := <-plugin.records:
			if !record.Failed {
				t.Fatalf("expected failed usage record")
			}
			if record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 4 {
				t.Fatalf("usage tokens = (%d, %d), want (12, 4)", record.Detail.InputTokens, record.Detail.OutputTokens)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for Gemini failed usage record")
		}
	})

	t.Run("vertex", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":15,\"candidatesTokenCount\":5,\"totalTokenCount\":20}}\n\n")
		}))
		defer server.Close()

		pluginName := "test-vertex-claude-truncation-usage"
		plugin := &captureAnyProviderUsagePlugin{provider: "vertex", records: make(chan usage.Record, 4)}
		usage.RegisterNamedPlugin(pluginName, plugin)
		t.Cleanup(func() {
			usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
		})

		exec := NewGeminiVertexExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "vertex",
			Attributes: map[string]string{
				"api_key":  "test-vertex-key",
				"base_url": server.URL,
			},
		}
		result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-2.5-flash",
			Payload: []byte(`{"model":"gemini-2.5-flash","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
			Stream:         true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for range result.Chunks {
		}

		select {
		case record := <-plugin.records:
			if !record.Failed {
				t.Fatalf("expected failed usage record")
			}
			if record.Detail.InputTokens != 15 || record.Detail.OutputTokens != 5 {
				t.Fatalf("usage tokens = (%d, %d), want (15, 5)", record.Detail.InputTokens, record.Detail.OutputTokens)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for Vertex failed usage record")
		}
	})

	t.Run("openai-compatibility", func(t *testing.T) {
		reply := "data: {\"id\":\"chatcmpl-trunc-usage\",\"object\":\"chat.completion.chunk\",\"created\":1773896263,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n\n"
		exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
		request := []byte(`{"model":"test","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)

		pluginName := "test-openai-compat-claude-truncation-usage"
		plugin := &captureAnyProviderUsagePlugin{provider: exec.Identifier(), records: make(chan usage.Record, 4)}
		usage.RegisterNamedPlugin(pluginName, plugin)
		t.Cleanup(func() {
			usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
		})

		result, err := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatClaude,
			ResponseFormat:  sdktranslator.FormatClaude,
			OriginalRequest: request,
			Stream:          true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for range result.Chunks {
		}

		select {
		case record := <-plugin.records:
			if !record.Failed {
				t.Fatalf("expected failed usage record")
			}
			if record.Detail.InputTokens != 9 || record.Detail.OutputTokens != 3 {
				t.Fatalf("usage tokens = (%d, %d), want (9, 3)", record.Detail.InputTokens, record.Detail.OutputTokens)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for OpenAICompat failed usage record")
		}
	})
}

func TestClaudeExecutorStreamCancel499RecordsLastPacketUsage(t *testing.T) {
	for _, mode := range []struct {
		name   string
		format sdktranslator.Format
	}{
		{name: "passthrough", format: sdktranslator.FormatClaude},
		{name: "translated", format: sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(mode.name, func(t *testing.T) {
			deltaFlushed := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher, _ := w.(http.Flusher)
				_, _ = fmt.Fprint(w, "event: message_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_cancel_499","type":"message","role":"assistant","content":[],"model":"claude-opus-5-5","stop_reason":null,"usage":{"input_tokens":4,"cache_creation_input_tokens":3036,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3036},"output_tokens":1,"service_tier":"standard","inference_geo":"global","speed":"standard"}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: message_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":6,"cache_creation_input_tokens":8846,"cache_read_input_tokens":3036,"output_tokens":120,"output_tokens_details":{"thinking_tokens":30},"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0},"iterations":[{"type":"message","input_tokens":4,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":3036,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":3036}},{"type":"message","input_tokens":2,"output_tokens":100,"cache_read_input_tokens":3036,"cache_creation_input_tokens":5810,"cache_creation":{"ephemeral_5m_input_tokens":5810,"ephemeral_1h_input_tokens":0}}]}}`+"\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				close(deltaFlushed)
				<-r.Context().Done()
			}))
			defer server.Close()

			pluginName := "test-claude-cancel-499-" + mode.name
			plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(pluginName, plugin)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
			})

			exec := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "claude-opus-5-5",
				Payload: []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   mode.format,
				ResponseFormat: mode.format,
				Stream:         true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			<-deltaFlushed
			// Drain until the message_delta chunk has been processed, then cancel before message_stop.
			for chunk := range result.Chunks {
				if strings.Contains(string(chunk.Payload), "message_delta") || strings.Contains(string(chunk.Payload), "partial") {
					cancel()
					break
				}
			}
			for range result.Chunks {
			}

			select {
			case record := <-plugin.records:
				if !record.Failed || record.Fail.StatusCode != 499 {
					t.Fatalf("expected failed=true status=499 on canceled stream, got failed=%v fail=%+v", record.Failed, record.Fail)
				}
				if record.Detail.InputTokens != 6 || record.Detail.OutputTokens != 120 || record.Detail.ReasoningTokens != 30 || record.Detail.CacheReadTokens != 3036 || record.Detail.CacheCreationTokens != 8846 {
					t.Fatalf("unexpected last-packet token counts on 499 cancel: %+v", record.Detail)
				}
				if record.Detail.CacheCreation5mTokens == nil || *record.Detail.CacheCreation5mTokens != 5810 || record.Detail.CacheCreation1hTokens == nil || *record.Detail.CacheCreation1hTokens != 3036 {
					t.Fatalf("expected 5m=5810 1h=3036 on 499 cancel, got 5m=%v 1h=%v", record.Detail.CacheCreation5mTokens, record.Detail.CacheCreation1hTokens)
				}
				if record.Detail.WebSearchRequests == nil || *record.Detail.WebSearchRequests != 1 {
					t.Fatalf("expected WebSearchRequests=1 on 499 cancel, got %v", record.Detail.WebSearchRequests)
				}
				if len(record.Detail.IterationSlice()) != 2 {
					t.Fatalf("expected 2 iterations on 499 cancel, got %d", len(record.Detail.IterationSlice()))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for 499 canceled usage record")
			}
		})
	}
}
