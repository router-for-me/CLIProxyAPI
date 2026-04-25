package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Say ok"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	gotContent := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if gotContent != "ok" {
		t.Fatalf("choices.0.message.content = %q, want %q; payload=%s", gotContent, "ok", string(resp.Payload))
	}
}

func TestCodexExecutorExecuteStream_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var completed []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if !bytes.HasPrefix(payload, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(payload[5:])
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			completed = append([]byte(nil), data...)
		}
	}

	if len(completed) == 0 {
		t.Fatal("missing response.completed chunk")
	}

	gotContent := gjson.GetBytes(completed, "response.output.0.content.0.text").String()
	if gotContent != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; completed=%s", gotContent, "ok", string(completed))
	}
}

func TestCodexExecutorExecuteStreamStopsWhenContextCancelledWithoutDraining(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for i := 0; i < helps.StreamChunkBufferSize*2; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"%d\"}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
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
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	cancel()
	deadline := time.After(time.Second)
	for {
		select {
		case _, ok := <-result.Chunks:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not stop after context cancellation")
		}
	}
}

func TestCodexExecutorExecuteStream_ResponseFailedBeforePayloadReturnsStatusErrorChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"error\":{\"type\":\"usage_limit_reached\",\"message\":\"You've hit your usage limit. Upgrade to Plus to continue using Codex.\",\"resets_in_seconds\":30}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatal("expected first chunk")
	}
	if chunk.Err == nil {
		t.Fatalf("expected error chunk, got payload=%q", string(chunk.Payload))
	}
	statusProvider, ok := chunk.Err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status provider, got %T", chunk.Err)
	}
	if got := statusProvider.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestCodexExecutorExecute_ResponseFailedAggregateReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"error\":{\"message\":\"You've hit your usage limit. Upgrade to Plus to continue using Codex.\",\"resets_in_seconds\":30}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Say ok"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err == nil {
		t.Fatal("expected Execute error")
	}

	statusProvider, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("expected status provider, got %T", err)
	}
	if got := statusProvider.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestPatchCodexCompletedOutputRecoversFunctionCall(t *testing.T) {
	streamState := newCodexStreamCompletionState()
	streamState.recordEvent([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_item_1","type":"function_call","call_id":"call_1","name":"search"}}`))
	streamState.recordEvent([]byte(`{"type":"response.function_call_arguments.done","item_id":"fc_item_1","output_index":0,"arguments":"{\"q\":\"hello\"}"}`))

	patched, recoveredCount := streamState.patchCompletedOutputIfEmpty([]byte(`{"response":{"output":[]}}`))
	if recoveredCount != 1 {
		t.Fatalf("recovered count = %d, want %d", recoveredCount, 1)
	}
	if got := gjson.GetBytes(patched, "response.output.0.type").String(); got != "function_call" {
		t.Fatalf("response.output.0.type = %q, want %q", got, "function_call")
	}
	if got := gjson.GetBytes(patched, "response.output.0.call_id").String(); got != "call_1" {
		t.Fatalf("response.output.0.call_id = %q, want %q", got, "call_1")
	}
	if got := gjson.GetBytes(patched, "response.output.0.arguments").String(); got != `{"q":"hello"}` {
		t.Fatalf("response.output.0.arguments = %q, want %q", got, `{"q":"hello"}`)
	}
}

func TestCollectCodexResponseAggregatePatchesCompletedOutputButKeepsCapturedBody(t *testing.T) {
	stream := strings.NewReader(
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\n",
	)

	result, err := collectCodexResponseAggregate(stream, true)
	if err != nil {
		t.Fatalf("collectCodexResponseAggregate() error = %v", err)
	}
	if got := gjson.GetBytes(result.completedData, "response.output.0.content.0.text").String(); got != "ok" {
		t.Fatalf("patched completed output text = %q, want %q", got, "ok")
	}
	if !strings.Contains(string(result.body), `"response":{"id":"resp_1","output":[]}`) {
		t.Fatalf("captured body did not preserve original completed event: %s", string(result.body))
	}
}
