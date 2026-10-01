package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravitySplitTerminalSSE reproduces the only upstream shape that makes
// FilterSSEUsageMetadata forward real usageMetadata on a chunk without
// finishReason: a chunk carrying finishReason but no usage, whose traceId is
// then matched by a usage-only tail chunk. The tail chunk is terminal, so the
// stream must end with exactly one finish_reason that carries the usage.
const antigravitySplitTerminalSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.7-flash","responseId":"resp-split"},"traceId":"trace-split"}

data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":""}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"modelVersion":"gemini-3.7-flash","responseId":"resp-split"},"traceId":"trace-split"}

`

// TestAntigravityStreamFinalizesSplitTerminalUsageOnce covers a terminal
// finishReason and its usage arriving in separate chunks: the stream must carry
// exactly one finish_reason, on the last chunk, together with the token counts.
func TestAntigravityStreamFinalizesSplitTerminalUsageOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, antigravitySplitTerminalSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var (
		chunks      [][]byte
		finishIndex = -1
	)
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		if reason := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String(); reason != "" {
			if finishIndex >= 0 {
				t.Fatalf("more than one terminal chunk: %s", chunk.Payload)
			}
			if reason != "stop" {
				t.Fatalf("finish_reason = %q, want stop", reason)
			}
			finishIndex = len(chunks)
		}
		chunks = append(chunks, chunk.Payload)
	}

	if finishIndex < 0 {
		t.Fatal("expected a terminal chunk")
	}
	if finishIndex != len(chunks)-1 {
		t.Fatalf("terminal chunk at index %d of %d chunks, want the last one", finishIndex, len(chunks))
	}
	terminal := chunks[finishIndex]
	if got := gjson.GetBytes(terminal, "usage.total_tokens").Int(); got != 33 {
		t.Fatalf("terminal total_tokens = %d, want 33", got)
	}
	if got := gjson.GetBytes(terminal, "usage.prompt_tokens").Int(); got != 11 {
		t.Fatalf("terminal prompt_tokens = %d, want 11", got)
	}
}

// antigravityResponsesSplitTerminalSSE is the Responses-shaped variant of the
// split terminal stream. With a traceId the usage filter forwards the tail
// untouched; without one it renames the tail usage to cpaUsageMetadata. Both
// must reach the terminal event with the token counts.
const antigravityResponsesSplitTerminalSSE = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-split-responses"},"traceId":"trace-split-responses"}

data: {"response":{"candidates":[{"finishReason":"STOP"}],"responseId":"resp-split-responses"},"traceId":"trace-split-responses"}

data: {"response":{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"thoughtsTokenCount":8,"totalTokenCount":41},"responseId":"resp-split-responses"},"traceId":"trace-split-responses"}

`

const antigravityResponsesSplitTerminalSSEWithoutTraceID = `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"first"}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-split-no-trace"}}

data: {"response":{"candidates":[{"finishReason":"STOP"}],"responseId":"resp-split-no-trace"}}

data: {"response":{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"thoughtsTokenCount":8,"totalTokenCount":41},"responseId":"resp-split-no-trace"}}

`

// runAntigravityResponsesStream feeds the SSE body through the Antigravity
// executor and returns the terminal events the client observed.
func runAntigravityResponsesStream(t *testing.T, body string) []gjson.Result {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	request := []byte(`{"model":"gemini-3.8-flash-high","input":"synthetic local fixture","stream":true}`)
	result, errExecute := NewAntigravityExecutor(&config.Config{RequestRetry: 1}).ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var terminals []gjson.Result
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch data.Get("type").String() {
			case "response.completed", "response.incomplete":
				terminals = append(terminals, data)
			}
		}
	}
	return terminals
}

func TestAntigravityResponsesStreamPreservesSplitTerminalUsage(t *testing.T) {
	for name, body := range map[string]string{
		"with trace id":    antigravityResponsesSplitTerminalSSE,
		"without trace id": antigravityResponsesSplitTerminalSSEWithoutTraceID,
	} {
		t.Run(name, func(t *testing.T) {
			terminals := runAntigravityResponsesStream(t, body)
			if len(terminals) != 1 {
				t.Fatalf("terminal events = %d, want 1", len(terminals))
			}
			terminal := terminals[0]
			if terminal.Get("type").String() != "response.completed" || terminal.Get("response.status").String() != "completed" {
				t.Fatalf("terminal = %s", terminal.Raw)
			}
			usage := terminal.Get("response.usage")
			if usage.Get("input_tokens").Int() != 11 || usage.Get("output_tokens").Int() != 30 ||
				usage.Get("output_tokens_details.reasoning_tokens").Int() != 8 || usage.Get("total_tokens").Int() != 41 {
				t.Fatalf("split terminal usage = %s", usage.Raw)
			}
			if strings.Contains(terminal.Raw, "cpaUsageMetadata") {
				t.Fatalf("internal usage carrier leaked downstream: %s", terminal.Raw)
			}
		})
	}
}

// TestAntigravityStreamReadErrorDoesNotSynthesizeCompletion pins the executor
// ordering this fork already had, so the direct Gemini path cannot drift back
// to translating [DONE] after a read failure.
func TestAntigravityStreamReadErrorDoesNotSynthesizeCompletion(t *testing.T) {
	body := antigravityResponsesSplitTerminalSSEWithoutTraceID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)+64))
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	request := []byte(`{"model":"gemini-3.8-flash-high","input":"synthetic local fixture","stream":true}`)
	result, errExecute := NewAntigravityExecutor(&config.Config{RequestRetry: 1}).ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash-high",
		Payload: request,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	terminals := 0
	streamErrors := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErrors++
			continue
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			switch gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:"))).Get("type").String() {
			case "response.completed", "response.incomplete":
				terminals++
			}
		}
	}
	if streamErrors != 1 {
		t.Fatalf("stream errors = %d, want 1", streamErrors)
	}
	if terminals != 0 {
		t.Fatalf("read error produced %d terminal events", terminals)
	}
}
