package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorExecuteStreamCancellationBeforeFirstEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if err != nil {
		cancel()
		t.Fatalf("ExecuteStream error: %v", err)
	}
	cancel()

	select {
	case chunk, ok := <-result.Chunks:
		if ok {
			t.Fatalf("received chunk after cancellation before first event: payload=%q err=%v", chunk.Payload, chunk.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled stream to close")
	}
}

func TestCodexExecutorExecuteStreamCancellationAfterPartialOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_cancel","model":"gpt-5.5","status":"in_progress","usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if err != nil {
		cancel()
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var first cliproxyexecutor.StreamChunk
	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			cancel()
			t.Fatal("stream closed before partial output")
		}
		first = chunk
	case <-time.After(time.Second):
		cancel()
		t.Fatal("timed out waiting for partial output")
	}
	cancel()

	chunks := []cliproxyexecutor.StreamChunk{first}
	deadline := time.After(time.Second)
	for {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				for _, got := range chunks {
					if got.Err != nil {
						t.Fatalf("cancellation emitted a synthetic stream error: %v", got.Err)
					}
					if bytes.Contains(got.Payload, []byte(`"type":"message_stop"`)) {
						t.Fatalf("cancellation emitted a synthetic successful terminal event: %s", got.Payload)
					}
				}
				return
			}
			chunks = append(chunks, chunk)
		case <-deadline:
			t.Fatal("timed out waiting for canceled partial stream to close")
		}
	}
}
