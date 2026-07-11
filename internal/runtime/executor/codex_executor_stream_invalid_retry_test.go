package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorExecuteStreamRetriesTerminalInvalidBeforeClientChunk(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"invalid encrypted content\"}}}\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"total_tokens\":6}}}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID: "auth-stream-invalid-retry-before-output",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	usageCollector := newCodexRetryUsageCollector(auth.ID)
	cliproxyusage.RegisterNamedPlugin("test-codex-stream-invalid-retry-before-output", usageCollector)

	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(
		context.Background(),
		auth,
		codexStreamInvalidRetryRequest(),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true},
	)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var payload bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("retried stream chunk error: %v", chunk.Err)
		}
		payload.Write(chunk.Payload)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("generation requests = %d, want exactly 2", got)
	}
	if !bytes.Contains(payload.Bytes(), []byte(`"delta":"ok"`)) {
		t.Fatalf("retried stream did not forward successful payload: %s", payload.Bytes())
	}
	records := usageCollector.waitFor(t, 2)
	if !records[0].Failed || records[1].Failed {
		t.Fatalf("generation retry usage outcomes = [%v, %v], want [failed, succeeded]", records[0].Failed, records[1].Failed)
	}
	usageCollector.assertNoAdditional(t)
}

func TestCodexExecutorExecuteStreamDoesNotRetryTerminalInvalidAfterClientChunk(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"invalid encrypted content\"}}}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID: "auth-stream-invalid-after-output",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	usageCollector := newCodexRetryUsageCollector(auth.ID)
	cliproxyusage.RegisterNamedPlugin("test-codex-stream-invalid-after-output", usageCollector)

	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(
		context.Background(),
		auth,
		codexStreamInvalidRetryRequest(),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true},
	)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var payload bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		payload.Write(chunk.Payload)
	}
	if !bytes.Contains(payload.Bytes(), []byte(`"delta":"partial"`)) {
		t.Fatalf("stream did not emit the pre-failure client chunk: %s", payload.Bytes())
	}
	if streamErr == nil {
		t.Fatal("missing terminal stream error")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("generation requests = %d, want exactly 1 after client-visible output", got)
	}
	records := usageCollector.waitFor(t, 1)
	if !records[0].Failed {
		t.Fatal("terminal stream failure usage record was not marked failed")
	}
	usageCollector.assertNoAdditional(t)
}

func TestCodexExecutorExecuteStreamTerminalInvalidRetryIsBounded(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"invalid_encrypted_content\",\"message\":\"invalid encrypted content\"}}}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID: "auth-stream-invalid-retry-bounded",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	usageCollector := newCodexRetryUsageCollector(auth.ID)
	cliproxyusage.RegisterNamedPlugin("test-codex-stream-invalid-retry-bounded", usageCollector)

	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(
		context.Background(),
		auth,
		codexStreamInvalidRetryRequest(),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true},
	)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("missing terminal stream error after bounded retry")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("generation requests = %d, want exactly 2", got)
	}
	records := usageCollector.waitFor(t, 2)
	if !records[0].Failed || !records[1].Failed {
		t.Fatalf("generation retry usage outcomes = [%v, %v], want two failures", records[0].Failed, records[1].Failed)
	}
	usageCollector.assertNoAdditional(t)
}

func codexStreamInvalidRetryRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna(xhigh)",
		Payload: []byte(`{"model":"gpt-5.6-luna(xhigh)","input":"hello"}`),
	}
}
