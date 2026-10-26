package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityExecutorExecuteStreamInteractionsEmitsDoneAfterSplitTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	terminalReceived := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// No traceId: FilterSSEUsageMetadata renames the split usage frame to
		// cpaUsageMetadata instead of forwarding it unchanged.
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"first\"}]},\"finishReason\":\"STOP\"}],\"modelVersion\":\"gemini-3.8-flash-high\",\"responseId\":\"resp-split\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]}}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":22,\"totalTokenCount\":33},\"modelVersion\":\"gemini-3.8-flash-high\",\"responseId\":\"resp-split\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the upstream open: the terminal events must be produced by the
		// usage frame itself, not by the executor's clean-EOF [DONE] tail.
		select {
		case <-terminalReceived:
		case <-r.Context().Done():
		}
	}))
	// Cancel before closing: on a regression the handler may still be waiting
	// for terminalReceived, and only the request cancellation releases it.
	t.Cleanup(func() {
		cancel()
		server.Close()
	})

	executor := NewAntigravityExecutor(&config.Config{Antigravity: config.AntigravityConfig{}, RequestRetry: 1})
	result, errExecute := executor.ExecuteStream(ctx, &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var chunks [][]byte
	doneSignaled := false
	deadline := time.After(30 * time.Second)
	chunksChan := result.Chunks
consume:
	for {
		select {
		case chunk, ok := <-chunksChan:
			if !ok {
				break consume
			}
			if chunk.Err != nil {
				t.Fatalf("unexpected stream error: %v", chunk.Err)
			}
			chunks = append(chunks, chunk.Payload)
			if !doneSignaled && bytes.Contains(chunk.Payload, []byte("event: done")) {
				doneSignaled = true
				close(terminalReceived)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the stream to terminate; done signaled = %v. Stream: %q", doneSignaled, bytes.Join(chunks, nil))
		}
	}
	if !doneSignaled {
		t.Fatalf("done event never arrived. Stream: %q", bytes.Join(chunks, nil))
	}
	joined := bytes.Join(chunks, nil)
	if got := bytes.Count(joined, []byte("event: done")); got != 1 {
		t.Fatalf("done event count = %d, want 1 before upstream close. Stream: %q", got, joined)
	}
	if got := bytes.Count(joined, []byte("data: [DONE]")); got != 1 {
		t.Fatalf("[DONE] count = %d, want 1. Stream: %q", got, joined)
	}
	if got := bytes.Count(joined, []byte("event: interaction.completed")); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Stream: %q", got, joined)
	}
	for _, line := range bytes.Split(joined, []byte("\n")) {
		payload := gjson.GetBytes(bytes.TrimSpace(line), "interaction.usage.total_tokens")
		if payload.Exists() && payload.Int() != 33 {
			t.Fatalf("completed total_tokens = %d, want 33. Stream: %q", payload.Int(), joined)
		}
	}
	if !bytes.Contains(joined, []byte(`"total_tokens":33`)) {
		t.Fatalf("completed usage missing from stream: %q", joined)
	}
}

func TestAntigravityExecutorExecuteStreamInteractionsCompletesToolCallWithoutFinishReason(t *testing.T) {
	// Mirrors a real upstream shape (see antigravity_executor_finish_reason_test.go):
	// a complete tool-call chunk with usage but no finishReason, ended by a
	// clean EOF. The Interactions stream must still terminate normally.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, antigravityNoFinishReasonSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatInteractions, sdktranslator.FormatInteractions,
		`{"model":"gemini-3.7-flash","input":"run ls","stream":true}`)
	joined := bytes.Join(chunks, nil)
	if got := bytes.Count(joined, []byte("event: interaction.completed")); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Stream: %q", got, joined)
	}
	if got := bytes.Count(joined, []byte("event: done")); got != 1 {
		t.Fatalf("done count = %d, want 1. Stream: %q", got, joined)
	}
	if got := bytes.Count(joined, []byte("data: [DONE]")); got != 1 {
		t.Fatalf("[DONE] count = %d, want 1. Stream: %q", got, joined)
	}
	if !bytes.Contains(joined, []byte(`"total_tokens":153720`)) {
		t.Fatalf("completed usage missing from stream: %q", joined)
	}
}

func TestAntigravityExecutorExecuteStreamInteractionsNoTerminalOnReadError(t *testing.T) {
	// A mid-stream connection drop (read error, not a clean EOF) must not be
	// reported as a successful completion, even for Interactions clients.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"totalTokenCount\":1}}}\n\n")
		flusher.Flush()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, errHijack := hijacker.Hijack()
		if errHijack == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{Antigravity: config.AntigravityConfig{}, RequestRetry: 1})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: []byte(`{"model":"gemini-3.8-flash-high","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var chunks [][]byte
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		chunks = append(chunks, chunk.Payload)
	}
	if streamErr == nil {
		t.Fatal("expected a stream error from the dropped connection")
	}
	joined := bytes.Join(chunks, nil)
	if bytes.Contains(joined, []byte("event: done")) || bytes.Contains(joined, []byte("[DONE]")) || bytes.Contains(joined, []byte("interaction.completed")) {
		t.Fatalf("dropped stream emitted a terminal event: %q", joined)
	}
}

func TestAntigravityExecutorExecuteStreamTranslatesInteractionsRequest(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:streamGenerateContent" {
			t.Fatalf("path = %q, want /v1internal:streamGenerateContent", r.URL.Path)
		}
		if gotAlt := r.URL.Query().Get("alt"); gotAlt != "sse" {
			t.Fatalf("alt = %q, want sse", gotAlt)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID:       "interactions-antigravity-stream-auth",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	payload := []byte(`{"model":"gemini-3.8-flash-high","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"get_weather","description":"weather","type":"function","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"stream":true,"store":false}`)
	result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatInteractions,
		ResponseFormat:  sdktranslator.FormatInteractions,
		Stream:          true,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if len(upstreamBody) == 0 {
		t.Fatal("upstream body was not captured")
	}

	for _, path := range []string{
		"request.stream",
		"request.generationConfig.toolChoice",
		"request.generationConfig.thinkingLevel",
		"request.generationConfig.thinkingSummaries",
	} {
		if gjson.GetBytes(upstreamBody, path).Exists() {
			t.Fatalf("%s should not be sent upstream: %s", path, string(upstreamBody))
		}
	}
	if gjson.GetBytes(upstreamBody, "input").Exists() {
		t.Fatalf("raw interactions input should not be sent upstream: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("request.toolConfig.functionCallingConfig.mode = %q, want AUTO. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("request.generationConfig.thinkingConfig.thinkingLevel = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("request.generationConfig.thinkingConfig.includeThoughts = false, want true. Body: %s", string(upstreamBody))
	}
}
