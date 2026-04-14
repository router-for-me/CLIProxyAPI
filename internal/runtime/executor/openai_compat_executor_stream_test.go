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

func TestOpenAICompatExecutorExecuteStream_ResponsesEmptyUpstreamSynthesizesCompletion(t *testing.T) {
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

	var gotCreated bool
	var gotCompleted bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		if hasOpenAIResponsesCompletedEvent(chunk.Payload) {
			gotCompleted = true
		}
		if hasOpenAIResponsesCreatedEvent(chunk.Payload) {
			gotCreated = true
		}
	}

	if !gotCreated {
		t.Fatal("expected synthetic response.created event")
	}
	if !gotCompleted {
		t.Fatal("expected synthetic response.completed event")
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

func TestOpenAICompatExecutorExecuteStream_OpenAISourceNoSynthesis(t *testing.T) {
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

	chunkCount := 0
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		chunkCount++
	}
	if chunkCount != 0 {
		t.Fatalf("expected no synthetic chunks for openai source format, got %d", chunkCount)
	}
}
