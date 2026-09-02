package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// antigravityNoFinishReasonSSE mirrors a real upstream stream captured from
// cloudcode-pa: a single tool-call chunk that carries usageMetadata but never
// emits finishReason.
const antigravityNoFinishReasonSSE = `data: {"response":{"candidates":[{"content":{"parts":[{"thoughtSignature":"CqcECqQEARFNMg8iZ3xVniKaulgBlzhJJDlc","functionCall":{"name":"bash","args":{"command":"ls -la .."},"id":"call_524881"}}],"role":"model"}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-nofr","usageMetadata":{"promptTokenCount":153609,"candidatesTokenCount":17,"totalTokenCount":153720,"thoughtsTokenCount":94}}}
`

func newAntigravityNoFinishReasonServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, antigravityNoFinishReasonSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
}

func collectAntigravityStream(t *testing.T, baseURL string, sourceFormat, responseFormat sdktranslator.Format, payload string) [][]byte {
	t.Helper()
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
		Attributes: map[string]string{"base_url": baseURL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(payload),
	}, cliproxyexecutor.Options{
		SourceFormat:   sourceFormat,
		ResponseFormat: responseFormat,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var chunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	return chunks
}

// TestAntigravityStreamDoesNotEmitClaudeMessageStopOnReadError locks in the
// executor ordering fix on the response format where it is observable: the
// Claude translator has always synthesized a terminal event on [DONE], so
// translating the tail before checking scanner.Err() reported a truncated
// upstream stream to Claude clients as a completed message.
func TestAntigravityStreamDoesNotEmitClaudeMessageStopOnReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]}}],"modelVersion":"gemini-3.7-flash","responseId":"resp-cut"}}`+"\n\n")
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
		Payload: []byte(`{"model":"gemini-3.7-flash","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if bytes.Contains(chunk.Payload, []byte("message_stop")) {
			t.Fatalf("read error must not emit message_stop: %s", chunk.Payload)
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream read error")
	}
}

func TestAntigravityStreamDoesNotFinalizeEmptyUpstreamStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	for _, tc := range []struct {
		name           string
		sourceFormat   sdktranslator.Format
		responseFormat sdktranslator.Format
		payload        string
	}{
		{"gemini", sdktranslator.FormatGemini, sdktranslator.FormatGemini,
			`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`},
		{"openai", sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI,
			`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat:   tc.sourceFormat,
				ResponseFormat: tc.responseFormat,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				if finish := gjson.GetBytes(chunk.Payload, "candidates.0.finishReason").String(); finish != "" {
					t.Fatalf("empty upstream stream must not synthesize a terminal chunk: %s", chunk.Payload)
				}
				if finish := gjson.GetBytes(chunk.Payload, "choices.0.finish_reason").String(); finish != "" {
					t.Fatalf("empty upstream stream must not synthesize a terminal chunk: %s", chunk.Payload)
				}
			}
		})
	}
}

func TestAntigravityStreamSynthesizesGeminiTerminalChunkWithUsage(t *testing.T) {
	server := newAntigravityNoFinishReasonServer(t)
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatGemini, sdktranslator.FormatGemini,
		`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)

	terminal := chunks[len(chunks)-1]
	if fr := gjson.GetBytes(terminal, "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("terminal finishReason = %q, want STOP", fr)
	}
	if role := gjson.GetBytes(terminal, "candidates.0.content.role").String(); role != "model" {
		t.Fatalf("terminal content role = %q, want model", role)
	}
	if got := gjson.GetBytes(terminal, "usageMetadata.totalTokenCount").Int(); got != 153720 {
		t.Fatalf("terminal totalTokenCount = %d, want 153720", got)
	}
	if got := gjson.GetBytes(terminal, "usageMetadata.promptTokenCount").Int(); got != 153609 {
		t.Fatalf("terminal promptTokenCount = %d, want 153609", got)
	}
	if got := gjson.GetBytes(terminal, "modelVersion").String(); got != "gemini-3.7-flash" {
		t.Fatalf("terminal modelVersion = %q, want gemini-3.7-flash", got)
	}
	if got := gjson.GetBytes(terminal, "responseId").String(); got != "resp-nofr" {
		t.Fatalf("terminal responseId = %q, want resp-nofr", got)
	}

	var finishReasonCount int
	for _, chunk := range chunks {
		if gjson.GetBytes(chunk, "candidates.0.finishReason").String() != "" {
			finishReasonCount++
		}
	}
	if finishReasonCount != 1 {
		t.Fatalf("expected exactly one finishReason chunk, got %d", finishReasonCount)
	}
}

func TestAntigravityStreamSynthesizesOpenAIToolCallsFinishReason(t *testing.T) {
	server := newAntigravityNoFinishReasonServer(t)
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAI,
		`{"model":"gemini-3.7-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`)

	terminal := chunks[len(chunks)-1]
	if fr := gjson.GetBytes(terminal, "choices.0.finish_reason").String(); fr != "tool_calls" {
		t.Fatalf("terminal finish_reason = %q, want tool_calls", fr)
	}
	if got := gjson.GetBytes(terminal, "usage.total_tokens").Int(); got != 153720 {
		t.Fatalf("terminal total_tokens = %d, want 153720", got)
	}
	if got := gjson.GetBytes(terminal, "usage.prompt_tokens").Int(); got != 153609 {
		t.Fatalf("terminal prompt_tokens = %d, want 153609", got)
	}
	if got := gjson.GetBytes(terminal, "model").String(); got != "gemini-3.7-flash" {
		t.Fatalf("terminal model = %q, want gemini-3.7-flash", got)
	}

	var finishReasonCount int
	for _, chunk := range chunks {
		if gjson.GetBytes(chunk, "choices.0.finish_reason").String() != "" {
			finishReasonCount++
		}
	}
	if finishReasonCount != 1 {
		t.Fatalf("expected exactly one finish_reason chunk, got %d", finishReasonCount)
	}
}
