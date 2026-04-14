package executor

import (
	"bufio"
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
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecuteStream_ReassemblesSplitCompletedFrame(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		_, _ = fmt.Fprint(w, "event: response.created\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1775540000,\"model\":\"gpt-5.3-codex\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}

		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1775540000,\"model\":\"gpt-5.3-codex\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15}}}")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": upstream.URL, "api_key": "test"}}
	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.3-codex",
		Payload: []byte(`{"model":"gpt-5.3-codex","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var sawCompleted bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		if hasOpenAIResponsesCompletedEvent(chunk.Payload) {
			sawCompleted = true
			if !strings.HasSuffix(string(chunk.Payload), "\n\n") {
				t.Fatalf("completed chunk missing SSE delimiter: %q", string(chunk.Payload))
			}
		}
	}

	if !sawCompleted {
		t.Fatal("expected response.completed event in stream")
	}
}

func TestReadCodexSSEFrame_ReassemblesDelimitedFrame(t *testing.T) {
	reader := strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	frame, err := readCodexSSEFrame(bufio.NewReader(reader))
	if err != nil {
		t.Fatalf("readCodexSSEFrame error: %v", err)
	}
	if got := gjson.GetBytes(codexSSEPayload(frame), "type").String(); got != "response.completed" {
		t.Fatalf("payload type = %q, want response.completed", got)
	}
	if !strings.HasSuffix(string(frame), "\n\n") {
		t.Fatalf("frame missing delimiter: %q", string(frame))
	}
}
