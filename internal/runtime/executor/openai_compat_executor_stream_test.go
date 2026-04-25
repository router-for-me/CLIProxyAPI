package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestOpenAICompatExecutorExecuteStream_ResponsesDoneWithoutFinishReasonStillCompletes(t *testing.T) {
	// Ensure clean capability resolver state for the empty auth ID used by this test.
	globalResponsesCapabilityResolver.Invalidate("")
	defer globalResponsesCapabilityResolver.Invalidate("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"id\":\"resp_done\",\"object\":\"chat.completion.chunk\",\"created\":1775540000,\"model\":\"glm-4.7\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": upstream.URL + "/v1",
			"api_key":  "test",
		},
	}
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.7",
		Payload: []byte(`{"model":"glm-4.7","input":"只回复OK","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotCompleted bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		if hasOpenAIResponsesCompletedEvent(chunk.Payload) {
			gotCompleted = true
		}
	}
	if !gotCompleted {
		t.Fatal("expected response.completed event in translated stream")
	}
}

func TestOpenAICompatExecutorExecuteStream_NativeResponsesPreservesFullSSEFrames(t *testing.T) {
	const authID = "test-native-preserve-sse"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_native\",\"created_at\":1775540000,\"model\":\"qwen3.6-plus\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"FetchUrl\",\"arguments\":\"\",\"status\":\"completed\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_native\",\"created_at\":1775540000,\"model\":\"qwen3.6-plus\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"FetchUrl\",\"arguments\":\"{}\",\"status\":\"completed\"}],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)
	defer globalResponsesCapabilityResolver.Invalidate(authID)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "Qwen3.6-Plus",
		Payload: []byte(`{"model":"Qwen3.6-Plus","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var chunks []string
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}

	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3; chunks=%q", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "event: response.created\n") {
		t.Fatalf("first chunk lost SSE event header: %q", chunks[0])
	}
	if !strings.Contains(chunks[1], "\nevent:") && !strings.HasPrefix(chunks[1], "event: response.output_item.added\n") {
		t.Fatalf("second chunk missing output_item.added SSE frame: %q", chunks[1])
	}
	if !strings.HasPrefix(chunks[2], "event: response.completed\n") {
		t.Fatalf("third chunk lost completed SSE event header: %q", chunks[2])
	}
	for i, chunk := range chunks {
		if !strings.HasSuffix(chunk, "\n\n") {
			t.Fatalf("chunk[%d] missing SSE frame delimiter: %q", i, chunk)
		}
	}
}

func TestOpenAICompatExecutorExecuteStream_ResponsesEmptyUpstreamReturnsTruncationError(t *testing.T) {
	// Ensure clean capability resolver state for the empty auth ID used by this test.
	globalResponsesCapabilityResolver.Invalidate("")
	defer globalResponsesCapabilityResolver.Invalidate("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Intentionally write nothing and close.
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": upstream.URL + "/v1",
			"api_key":  "test",
		},
	}
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.7",
		Payload: []byte(`{"model":"glm-4.7","input":"只回复OK","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}

	if gotErr == nil {
		t.Fatal("expected truncation error for empty upstream responses stream")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "truncated before first chunk") {
		t.Fatalf("error = %v, want truncation classification", gotErr)
	}
	statusCoder, ok := gotErr.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode()", gotErr)
	}
	if statusCoder.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", statusCoder.StatusCode(), http.StatusBadGateway)
	}
}

func TestSynthesizeOpenAIResponsesCompletion_UsesValidSSEDelimiter(t *testing.T) {
	chunks := synthesizeOpenAIResponsesCompletion("glm-4.7")
	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasSuffix(string(chunk), "\n\n") {
			t.Fatalf("chunk[%d] missing SSE frame delimiter: %q", i, string(chunk))
		}
	}
}

func TestOpenAICompatExecutorExecuteStream_OpenAISourceEmptyUpstreamReturnsTruncationError(t *testing.T) {
	// Ensure clean capability resolver state for the empty auth ID used by this test.
	globalResponsesCapabilityResolver.Invalidate("")
	defer globalResponsesCapabilityResolver.Invalidate("")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Intentionally empty upstream.
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": upstream.URL + "/v1",
			"api_key":  "test",
		},
	}
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.7",
		Payload: []byte(`{"model":"glm-4.7","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotErr error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatal("expected truncation error for empty upstream chat stream")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "truncated before first chunk") {
		t.Fatalf("error = %v, want truncation classification", gotErr)
	}
}

func TestOpenAICompatExecutorExecuteStream_OpenAISourceTruncatedChunkedBodySynthesizesDone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		defer conn.Close()

		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = rw.WriteString("Content-Type: text/event-stream\r\n")
		_, _ = rw.WriteString("Transfer-Encoding: chunked\r\n")
		_, _ = rw.WriteString("\r\n")
		frame := "data: {\"id\":\"chatcmpl_partial\",\"object\":\"chat.completion.chunk\",\"created\":1775540000,\"model\":\"glm-4.7\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n"
		_, _ = rw.WriteString(fmt.Sprintf("%x\r\n%s\r\n", len(frame), frame))
		_, _ = rw.WriteString("10\r\npartial")
		_ = rw.Flush()
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": upstream.URL + "/v1",
			"api_key":  "test",
		},
	}
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.7",
		Payload: []byte(`{"model":"glm-4.7","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotChunkErr error
	var chunkCount int
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotChunkErr = chunk.Err
			break
		}
		chunkCount++
	}
	if gotChunkErr != nil {
		t.Fatalf("expected tolerated truncated chunked stream, got chunk err: %v", gotChunkErr)
	}
	if chunkCount == 0 {
		t.Fatal("expected partial stream payloads after tolerated stream truncation")
	}
}

func TestOpenAICompatExecutorExecuteStream_ResponsesTruncatedChunkedBodyStillCompletes(t *testing.T) {
	const authID = "test-native-truncated-responses"
	globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)
	defer globalResponsesCapabilityResolver.Invalidate(authID)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijacking")
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			t.Fatalf("Hijack() error = %v", err)
		}
		defer conn.Close()

		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n")
		_, _ = rw.WriteString("Content-Type: text/event-stream\r\n")
		_, _ = rw.WriteString("Transfer-Encoding: chunked\r\n")
		_, _ = rw.WriteString("\r\n")
		frame := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_partial\",\"created_at\":1775540000,\"model\":\"glm-4.7\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
		_, _ = rw.WriteString(fmt.Sprintf("%x\r\n%s\r\n", len(frame), frame))
		_, _ = rw.WriteString("10\r\npartial")
		_ = rw.Flush()
	}))
	defer upstream.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := testAuthForServer(upstream.URL, authID)
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-4.7",
		Payload: []byte(`{"model":"glm-4.7","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	if stream == nil {
		t.Fatal("stream result is nil")
	}

	var gotChunkErr error
	var gotCompleted bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			gotChunkErr = chunk.Err
			break
		}
		if hasOpenAIResponsesCompletedEvent(chunk.Payload) {
			gotCompleted = true
		}
	}
	if gotChunkErr != nil {
		t.Fatalf("expected tolerated truncated chunked responses stream, got chunk err: %v", gotChunkErr)
	}
	if !gotCompleted {
		t.Fatal("expected response.completed event after tolerated stream truncation")
	}
}
