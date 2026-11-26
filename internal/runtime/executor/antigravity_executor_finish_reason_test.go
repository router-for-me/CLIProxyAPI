package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

func TestAntigravityStreamSurfacesTrailingBackendError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"modelVersion":"gemini-3.7-flash"}}

{
  "error": {
    "code": 503,
    "message": "backend capacity exhausted",
    "status": "UNAVAILABLE"
  }
}
`)
	}))
	defer server.Close()

	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		t.Run(format.String(), func(t *testing.T) {
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
				Payload: []byte(`{"model":"gemini-3.7-flash","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   format,
				ResponseFormat: format,
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
				if bytes.Contains(chunk.Payload, []byte("message_stop")) || bytes.Contains(chunk.Payload, []byte(`"finish_reason":"stop"`)) {
					t.Fatalf("trailing backend error became successful completion: %s", chunk.Payload)
				}
			}
			if streamErr == nil || !bytes.Contains([]byte(streamErr.Error()), []byte("backend capacity exhausted")) {
				t.Fatalf("trailing backend error = %v, want surfaced backend message", streamErr)
			}
			statusError, ok := streamErr.(interface{ StatusCode() int })
			if !ok || statusError.StatusCode() != http.StatusServiceUnavailable {
				t.Fatalf("trailing backend status code = %v, want %d", streamErr, http.StatusServiceUnavailable)
			}
		})
	}
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

func TestAntigravityStreamRejectsEmptyUpstreamStream(t *testing.T) {
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
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				if len(chunk.Payload) != 0 {
					t.Fatalf("empty upstream stream emitted payload: %s", chunk.Payload)
				}
			}
			statusError, ok := streamErr.(interface{ StatusCode() int })
			if !ok || statusError.StatusCode() != http.StatusBadGateway {
				t.Fatalf("empty upstream stream error = %v, want 502", streamErr)
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
	if fr := ojson.GetBytes(terminal, "candidates.0.finishReason").String(); fr != "STOP" {
		t.Fatalf("terminal finishReason = %q, want STOP", fr)
	}
	if role := ojson.GetBytes(terminal, "candidates.0.content.role").String(); role != "model" {
		t.Fatalf("terminal content role = %q, want model", role)
	}
	if got := ojson.GetBytes(terminal, "usageMetadata.totalTokenCount").Int(); got != 153720 {
		t.Fatalf("terminal totalTokenCount = %d, want 153720", got)
	}
	if got := ojson.GetBytes(terminal, "usageMetadata.promptTokenCount").Int(); got != 153609 {
		t.Fatalf("terminal promptTokenCount = %d, want 153609", got)
	}
	if got := ojson.GetBytes(terminal, "modelVersion").String(); got != "gemini-3.7-flash" {
		t.Fatalf("terminal modelVersion = %q, want gemini-3.7-flash", got)
	}
	if got := ojson.GetBytes(terminal, "responseId").String(); got != "resp-nofr" {
		t.Fatalf("terminal responseId = %q, want resp-nofr", got)
	}

	var finishReasonCount int
	for _, chunk := range chunks {
		if ojson.GetBytes(chunk, "candidates.0.finishReason").String() != "" {
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
	if fr := ojson.GetBytes(terminal, "choices.0.finish_reason").String(); fr != "tool_calls" {
		t.Fatalf("terminal finish_reason = %q, want tool_calls", fr)
	}
	if got := ojson.GetBytes(terminal, "usage.total_tokens").Int(); got != 153720 {
		t.Fatalf("terminal total_tokens = %d, want 153720", got)
	}
	if got := ojson.GetBytes(terminal, "usage.prompt_tokens").Int(); got != 153609 {
		t.Fatalf("terminal prompt_tokens = %d, want 153609", got)
	}
	if got := ojson.GetBytes(terminal, "model").String(); got != "gemini-3.7-flash" {
		t.Fatalf("terminal model = %q, want gemini-3.7-flash", got)
	}

	var finishReasonCount int
	for _, chunk := range chunks {
		if ojson.GetBytes(chunk, "choices.0.finish_reason").String() != "" {
			finishReasonCount++
		}
	}
	if finishReasonCount != 1 {
		t.Fatalf("expected exactly one finish_reason chunk, got %d", finishReasonCount)
	}
}

func TestAntigravityAssemblyReplacesOnlyContentParts(t *testing.T) {
	for _, metadata := range []string{
		`"groundingMetadata":{"webSearchQueries":[]}`,
		`"marker":"__cpa_parts_placeholder__"`,
	} {
		t.Run(metadata, func(t *testing.T) {
			raw := []byte(`{"response":{"candidates":[{` + metadata + `,"content":{"role":"model","parts":[]},"finishReason":"STOP"}]}}`)
			exec := &AntigravityExecutor{}
			first := []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}}`)
			out := exec.convertStreamChunksToNonStream([][]byte{first, raw})
			if !ojson.ValidBytes(out) || ojson.GetBytes(out, "response.candidates.0.content.parts.0.text").String() != "hello" {
				t.Fatalf("assembled parts replaced the wrong value: %s", out)
			}
			before := ojson.GetBytes(raw, "response.candidates.0")
			after := ojson.GetBytes(out, "response.candidates.0")
			if before.Get("groundingMetadata").Raw != after.Get("groundingMetadata").Raw ||
				before.Get("marker").Raw != after.Get("marker").Raw {
				t.Fatalf("assembly changed unrelated metadata: %s", out)
			}
		})
	}
}

func TestAntigravityAssemblyPreservesGroundingMetadataFromEarlierChunk(t *testing.T) {
	exec := &AntigravityExecutor{}
	chunks := [][]byte{
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Search result answer."}]},"groundingMetadata":{"webSearchQueries":["example query"],"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":6},"groundingChunkIndices":[0]}]}}],"modelVersion":"gemini-3-pro","responseId":"resp-ground-1"}}`),
		[]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17}}}`),
	}

	out := exec.convertStreamChunksToNonStream(chunks)
	grounding := ojson.GetBytes(out, "response.candidates.0.groundingMetadata")
	if !grounding.Exists() {
		t.Fatalf("assembled response dropped groundingMetadata from earlier chunk: %s", out)
	}
	if got := grounding.Get("webSearchQueries.0").String(); got != "example query" {
		t.Fatalf("grounding web search query = %q, want %q; out=%s", got, "example query", out)
	}
	if got := grounding.Get("groundingChunks.0.web.uri").String(); got != "https://example.com" {
		t.Fatalf("grounding chunk URI = %q, want %q; out=%s", got, "https://example.com", out)
	}
	groundingIndex := grounding.Get("groundingSupports.0.groundingChunkIndices.0")
	if !groundingIndex.Exists() || groundingIndex.Raw != "0" {
		t.Fatalf("grounding support index = %q, want 0; out=%s", groundingIndex.Raw, out)
	}
}

func TestAntigravityStreamPreservesQuotedFilterNoticeWithUsageMetadata(t *testing.T) {
	quotedNotice := "This request was blocked by Gemini's filters. They can occasionally trigger by mistake on safe coding, security, or biology-related queries. Please try rephrasing your prompt. You can [send feedback](https://ai.google.dev/gemini-api/docs/troubleshooting#file-bug) or read more about [our policies here](https://policies.google.com/terms/generative-ai/use-policy)."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"`+quotedNotice+`"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":40,"totalTokenCount":52}}}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	chunks := collectAntigravityStream(t, server.URL, sdktranslator.FormatGemini, sdktranslator.FormatGemini,
		`{"contents":[{"role":"user","parts":[{"text":"explain the filter message"}]}]}`)
	var joined bytes.Buffer
	for _, chunk := range chunks {
		joined.Write(chunk)
	}
	if !bytes.Contains(joined.Bytes(), []byte(quotedNotice)) {
		t.Fatalf("expected quoted filter notice in stream output, got %s", joined.String())
	}
}

func TestAntigravityStreamCleanEOFWithoutFinishReasonCommitsReasoningReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3.8-flash-high"
		sessionID = "clean-eof-stream-replay"
	)
	signature := strings.Repeat("c", 128)
	noFinishReasonSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"name":"bash","args":{"command":"ls"},"id":"call_clean_eof_1"}}]}}],"modelVersion":"` + model + `","responseId":"resp-clean-eof-1","usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":15,"totalTokenCount":135,"thoughtsTokenCount":20}}}` + "\n\n"

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-clean-eof-stream")

	turn1Ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(noFinishReasonSSE)),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	turn1Body := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"bash","description":"run shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"messages":[{"role":"user","content":"list files"}]}`
	stream1, errStream1 := exec.ExecuteStream(turn1Ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(turn1Body),
	}, opts)
	if errStream1 != nil {
		t.Fatalf("Turn 1 ExecuteStream() error = %v", errStream1)
	}

	var sawMessageStop bool
	for chunk := range stream1.Chunks {
		if chunk.Err != nil {
			t.Fatalf("Turn 1 unexpected stream error: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), `"type":"message_stop"`) {
			sawMessageStop = true
		}
	}
	if !sawMessageStop {
		t.Fatal("expected Turn 1 clean-EOF stream to deliver synthetic message_stop")
	}

	items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID)
	if !ok || len(items) == 0 {
		t.Fatalf("clean-EOF tool stream committed 0 reasoning replay items (ok=%v)", ok)
	}
	var hasSig bool
	for _, item := range items {
		if ojson.GetBytesNoCopy(item, "thoughtSignature").String() == signature {
			hasSig = true
			break
		}
	}
	if !hasSig {
		t.Fatalf("committed replay items did not contain expected thoughtSignature: %q", items)
	}

	var capturedTurn2Request []byte
	turn2Ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		raw, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			return nil, errRead
		}
		capturedTurn2Request = append([]byte(nil), raw...)
		respSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17},"modelVersion":"` + model + `","responseId":"resp-clean-eof-turn2"}}` + "\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(respSSE)),
		}, nil
	}))

	turn2Body := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"bash","description":"run shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"messages":[{"role":"user","content":"list files"},{"role":"assistant","content":[{"type":"tool_use","id":"call_clean_eof_1","name":"bash","input":{"command":"ls"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_clean_eof_1","content":"README.md"}]}]}`
	stream2, errStream2 := exec.ExecuteStream(turn2Ctx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(turn2Body),
	}, opts)
	if errStream2 != nil {
		t.Fatalf("Turn 2 ExecuteStream() error = %v", errStream2)
	}
	for chunk := range stream2.Chunks {
		if chunk.Err != nil {
			t.Fatalf("Turn 2 stream error: %v", chunk.Err)
		}
	}

	var restored bool
	for _, content := range ojson.GetBytesNoCopy(capturedTurn2Request, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall.name").String() == "bash" && part.Get("thoughtSignature").String() == signature {
				restored = true
			}
		}
	}
	if !restored {
		t.Fatalf("Turn 2 missed thoughtSignature replay after Turn 1 clean-EOF tool stream; upstream request = %s", string(capturedTurn2Request))
	}
}

func TestAntigravityExecuteClaudeNonStreamCleanEOFWithoutFinishReasonCommitsReasoningReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3-pro"
		sessionID = "clean-eof-nonstream-replay"
	)
	signature := strings.Repeat("p", 128)
	noFinishReasonSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"name":"bash","args":{"command":"pwd"},"id":"call_clean_eof_ns_1"}}]}}],"modelVersion":"` + model + `","responseId":"resp-clean-eof-ns-1","usageMetadata":{"promptTokenCount":80,"candidatesTokenCount":10,"totalTokenCount":90,"thoughtsTokenCount":12}}}` + "\n\n"

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-clean-eof-nonstream")

	reqCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(noFinishReasonSSE)),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         false,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":false,"tools":[{"name":"bash","description":"run shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"messages":[{"role":"user","content":"print working directory"}]}`
	resp, errExec := exec.Execute(reqCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload")
	}

	items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID)
	if !ok || len(items) == 0 {
		t.Fatalf("clean-EOF executeClaudeNonStream committed 0 reasoning replay items (ok=%v)", ok)
	}
	var hasSig bool
	for _, item := range items {
		if ojson.GetBytesNoCopy(item, "thoughtSignature").String() == signature {
			hasSig = true
			break
		}
	}
	if !hasSig {
		t.Fatalf("committed replay items did not contain expected thoughtSignature: %q", items)
	}
}

func TestAntigravityStreamCleanEOFIncompleteStreamingFunctionCallDoesNotCommitReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3.8-flash-high"
		sessionID = "clean-eof-incomplete-fc"
	)
	signature := strings.Repeat("i", 128)
	incompleteFC := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"id":"call_incomplete","name":"get_weather","partialArgs":[{"jsonPath":"$.city","stringValue":"New ","willContinue":true}],"willContinue":true}}]}}],"modelVersion":"` + model + `","responseId":"resp-incomplete-fc","usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"totalTokenCount":25}}}` + "\n\n"

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-clean-eof-incomplete")

	reqCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(incompleteFC)),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"get_weather","description":"get weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[{"role":"user","content":"weather in NY"}]}`
	stream, errStream := exec.ExecuteStream(reqCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	if items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID); ok || len(items) != 0 {
		t.Fatalf("incomplete streaming functionCall on clean EOF must not commit replay items, got ok=%v items=%d", ok, len(items))
	}
}

func TestAntigravityReasoningReplayMarkCleanEOFRequiresObservedCandidate(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	scope := antigravityReasoningReplayScope{modelName: "gemini-3.8-flash-high", sessionKey: "session:mark-clean-eof-empty"}
	existing := []byte(`{"type":"thought_signature","contentIndex":1,"partIndex":0,"thoughtSignature":"existing-signature-123456789"}`)
	internalcache.CacheAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey, [][]byte{existing})

	reqPayload := []byte(`{"sessionId":"mark-clean-eof-empty","request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, reqPayload)
	acc.ObserveSSELine([]byte(`data: {"response":{"usageMetadata":{"promptTokenCount":10}}}`))
	acc.MarkCleanEOF()
	acc.Commit(context.Background())

	if items, ok := internalcache.GetAntigravityReasoningReplayItems(scope.modelName, scope.sessionKey); !ok || len(items) != 1 {
		t.Fatalf("MarkCleanEOF without observed candidate must not commit or wipe existing cache, got ok=%v items=%d", ok, len(items))
	}
}

func TestAntigravityExecuteClaudeNonStreamEmptyCandidatePreservesExistingReplayCache(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3-pro"
		sessionID = "clean-eof-empty-candidate"
	)
	existing := []byte(`{"type":"thought_signature","contentIndex":1,"partIndex":0,"thoughtSignature":"existing-signature-123456789"}`)
	internalcache.CacheAntigravityReasoningReplayItems(model, "responses:"+sessionID, [][]byte{existing})

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-empty-candidate")

	reqCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(`data: {"response":{"candidates":[{}]}}` + "\n\n")),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         false,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":false,"messages":[{"role":"user","content":"hello"}]}`
	_, errExec := exec.Execute(reqCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID)
	if !ok || len(items) != 1 || ojson.GetBytesNoCopy(items[0], "thoughtSignature").String() != "existing-signature-123456789" {
		t.Fatalf("empty candidate without finishReason must not wipe existing replay cache, got ok=%v items=%q", ok, items)
	}
}

func TestAntigravityStreamCleanEOFWithTrailingIncompleteSSEFrameDoesNotCommitReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3.8-flash-high"
		sessionID = "clean-eof-trailing-incomplete-stream"
	)
	signature := strings.Repeat("t", 128)
	truncatedStream := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"name":"bash","args":{"command":"ls"},"id":"call_trunc_1"}}]}}],"modelVersion":"` + model + `","responseId":"resp-trunc-1","usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":10,"totalTokenCount":60}}}` + "\n\n" +
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"cut off`

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-trailing-incomplete-stream")

	reqCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(truncatedStream)),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"bash","description":"run shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"messages":[{"role":"user","content":"list files"}]}`
	stream, errStream := exec.ExecuteStream(reqCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	if items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID); ok || len(items) != 0 {
		t.Fatalf("stream with trailing incomplete SSE JSON frame must not commit replay items, got ok=%v items=%d", ok, len(items))
	}
}

func TestAntigravityExecuteClaudeNonStreamCleanEOFWithTrailingIncompleteSSEFrameDoesNotCommitReplay(t *testing.T) {
	internalcache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(internalcache.ClearAntigravityReasoningReplayCache)

	const (
		model     = "gemini-3-pro"
		sessionID = "clean-eof-trailing-incomplete-nonstream"
	)
	signature := strings.Repeat("n", 128)
	truncatedStream := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"` + signature + `","functionCall":{"name":"bash","args":{"command":"pwd"},"id":"call_trunc_ns_1"}}]}}],"modelVersion":"` + model + `","responseId":"resp-trunc-ns-1","usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":10,"totalTokenCount":60}}}` + "\n\n" +
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"cut off`

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-trailing-incomplete-nonstream")

	reqCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(truncatedStream)),
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         false,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":false,"tools":[{"name":"bash","description":"run shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],"messages":[{"role":"user","content":"print working directory"}]}`
	_, errExec := exec.Execute(reqCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	if items, ok := internalcache.GetAntigravityReasoningReplayItems(model, "responses:"+sessionID); ok || len(items) != 0 {
		t.Fatalf("executeClaudeNonStream with trailing incomplete SSE JSON frame must not commit replay items, got ok=%v items=%d", ok, len(items))
	}
}
