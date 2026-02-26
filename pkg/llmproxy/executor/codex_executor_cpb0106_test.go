package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"
	cliproxyauth "github.com/kooshapari/cliproxyapi-plusplus/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/cliproxyapi-plusplus/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/cliproxyapi-plusplus/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

const cpb0106CodexSSECompletedEvent = `data: {"type":"response.completed","response":{"id":"resp_0106","object":"response","status":"completed","created_at":1735689600,"model":"gpt-5.3-codex","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":25,"output_tokens":8,"total_tokens":33}}}`

func loadFixture(t *testing.T, relativePath string) []byte {
	t.Helper()
	path := filepath.Join("testdata", relativePath)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read fixture %q: %v", path, err)
	}
	return b
}

func TestCodexExecutor_VariantOnlyRequest_PassesReasoningForExecute(t *testing.T) {
	payload := loadFixture(t, filepath.ToSlash("cpb-0106-variant-only-openwork-chat-completions.json"))

	requestBodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodyCh <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(cpb0106CodexSSECompletedEvent))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "cpb0106",
		},
	}
	reqPayload := []byte(payload)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload")
	}

	var upstreamBody []byte
	select {
	case upstreamBody = <-requestBodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not capture upstream request body in time")
	}

	out := gjson.GetBytes(upstreamBody, "stream")
	if !out.Exists() || !out.Bool() {
		t.Fatalf("expected upstream stream=true, got %v", out.Bool())
	}
	if got := gjson.GetBytes(upstreamBody, "reasoning.effort").String(); got != "high" {
		t.Fatalf("expected reasoning.effort=high, got %q", got)
	}
}

func TestCodexExecutor_VariantOnlyRequest_PassesReasoningForExecuteStream(t *testing.T) {
	payload := loadFixture(t, filepath.ToSlash("cpb-0106-variant-only-openwork-chat-completions.json"))

	requestBodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodyCh <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(cpb0106CodexSSECompletedEvent))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "cpb0106",
		},
	}
	reqPayload := []byte(payload)

	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: reqPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream failed: %v", err)
	}

	chunkCount := 0
	for chunk := range streamResult.Chunks {
		if len(chunk.Payload) > 0 {
			chunkCount++
		}
	}
	if chunkCount == 0 {
		t.Fatal("expected stream result to emit chunks")
	}

	var upstreamBody []byte
	select {
	case upstreamBody = <-requestBodyCh:
	case <-time.After(2 * time.Second):
		t.Fatal("did not capture upstream request body in time")
	}

	if got := gjson.GetBytes(upstreamBody, "stream").Bool(); got != false {
		t.Fatalf("expected upstream stream=false in ExecuteStream path, got %v", got)
	}
	if got := gjson.GetBytes(upstreamBody, "reasoning.effort").String(); got != "high" {
		t.Fatalf("expected reasoning.effort=high, got %q", got)
	}
}
