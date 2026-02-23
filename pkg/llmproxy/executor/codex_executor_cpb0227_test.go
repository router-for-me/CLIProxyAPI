package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestCodexExecutor_CPB0227_ExecuteFailsWhenStreamClosesBeforeResponseCompleted(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.in_progress\"}\n")
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "cpb0227"}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"ping"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if err == nil {
		t.Fatal("expected Execute to fail when response.completed is missing")
	}

	var got statusErr
	if !errors.As(err, &got) {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if got.code != 408 {
		t.Fatalf("expected status 408, got %d", got.code)
	}
	if !strings.Contains(got.msg, "stream closed before response.completed") {
		t.Fatalf("expected completion-missing message, got %q", got.msg)
	}
}

func TestCodexExecutor_CPB0227_ExecuteStreamEmitsErrorWhenResponseCompletedMissing(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n")
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "cpb0227"}}

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"role":"user","content":"ping"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}

	var streamErr error
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream error chunk when response.completed is missing")
	}

	var got statusErr
	if !errors.As(streamErr, &got) {
		t.Fatalf("expected statusErr from stream, got %T: %v", streamErr, streamErr)
	}
	if got.code != 408 {
		t.Fatalf("expected status 408, got %d", got.code)
	}
	if !strings.Contains(got.msg, "stream closed before response.completed") {
		t.Fatalf("expected completion-missing message, got %q", got.msg)
	}
}
