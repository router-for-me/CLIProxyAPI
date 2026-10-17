package executor

import (
	"bytes"
	"context"
	"fmt"
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial vertex\"}]}}]}\n\n")
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
		t.Fatalf("expected error chunk on truncated Vertex-to-Claude stream missing finishReason, got nil; output=%q", output.String())
	}
	if strings.Contains(output.String(), "message_stop") {
		t.Fatalf("truncated Vertex-to-Claude stream must not synthesize message_stop: %q", output.String())
	}
}
