package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestCodexExecutorNonStreamExtractsContentFromDoneEvents verifies that a
// non-streaming Execute call correctly returns message content even when the
// response.completed event carries an empty "output" array. The actual content
// arrives via response.output_item.done events earlier in the SSE stream.
//
// This is a regression test for https://github.com/router-for-me/CLIProxyAPI/issues/2582
func TestCodexExecutorNonStreamExtractsContentFromDoneEvents(t *testing.T) {
	// Simulate realistic Codex SSE stream where response.completed has empty output
	ssePayload := "" +
		"event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_test","object":"response","created_at":1700000000,"status":"in_progress","model":"gpt-5.4","output":[]}}` + "\n\n" +
		"event: response.output_item.added\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_test","type":"message","status":"in_progress","content":[],"role":"assistant"}}` + "\n\n" +
		"event: response.content_part.added\n" +
		`data: {"type":"response.content_part.added","content_index":0,"item_id":"msg_test","output_index":0,"part":{"type":"output_text","text":""}}` + "\n\n" +
		"event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","content_index":0,"delta":"Hello!","item_id":"msg_test","output_index":0}` + "\n\n" +
		"event: response.output_text.done\n" +
		`data: {"type":"response.output_text.done","content_index":0,"item_id":"msg_test","output_index":0,"text":"Hello!"}` + "\n\n" +
		"event: response.content_part.done\n" +
		`data: {"type":"response.content_part.done","content_index":0,"item_id":"msg_test","output_index":0,"part":{"type":"output_text","text":"Hello!"}}` + "\n\n" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_test","type":"message","status":"completed","content":[{"type":"output_text","text":"Hello!"}],"role":"assistant"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_test","object":"response","created_at":1700000000,"status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":8,"output_tokens":6,"total_tokens":14}}}` + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"say hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// The executor injects output items from response.output_item.done into
	// the response.completed event. Verify the output array is populated.
	output := gjson.GetBytes(resp.Payload, "response.output")
	if !output.IsArray() || len(output.Array()) == 0 {
		t.Fatalf("expected non-empty response.output, got: %s", string(resp.Payload))
	}

	outputText := gjson.GetBytes(resp.Payload, "response.output.0.content.0.text").String()
	if outputText != "Hello!" {
		t.Fatalf("expected output text %q, got %q (full payload: %s)", "Hello!", outputText, string(resp.Payload))
	}
}

// TestCodexExecutorNonStreamPreservesPopulatedOutput verifies that when the
// response.completed event already contains a populated output array, the
// fix does not overwrite it.
func TestCodexExecutorNonStreamPreservesPopulatedOutput(t *testing.T) {
	// Simulate a response where response.completed already has output populated
	ssePayload := "" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"msg_test","type":"message","status":"completed","content":[{"type":"output_text","text":"Hi there!"}],"role":"assistant"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_test","object":"response","created_at":1700000000,"status":"completed","model":"gpt-5.4","output":[{"id":"msg_test","type":"message","status":"completed","content":[{"type":"output_text","text":"Hi there!"}],"role":"assistant"}],"usage":{"input_tokens":5,"output_tokens":4,"total_tokens":9}}}` + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// When response.completed already has output populated, it should be preserved.
	outputText := gjson.GetBytes(resp.Payload, "response.output.0.content.0.text").String()
	if outputText != "Hi there!" {
		t.Fatalf("expected output text %q, got %q (full payload: %s)", "Hi there!", outputText, string(resp.Payload))
	}
}

// TestCodexExecutorNonStreamExtractsToolCallsFromDoneEvents verifies that
// function_call output items from response.output_item.done events are
// correctly injected into the non-streaming response.
func TestCodexExecutorNonStreamExtractsToolCallsFromDoneEvents(t *testing.T) {
	ssePayload := "" +
		"event: response.output_item.done\n" +
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"fc_test","type":"function_call","status":"completed","call_id":"call_123","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_test","object":"response","created_at":1700000000,"status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":10,"output_tokens":15,"total_tokens":25}}}` + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(ssePayload))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"weather in Beijing"}],"tools":[{"type":"function","function":{"name":"get_weather"}}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	// Verify function_call output items were injected
	output := gjson.GetBytes(resp.Payload, "response.output")
	if !output.IsArray() || len(output.Array()) == 0 {
		t.Fatalf("expected non-empty response.output, got: %s", string(resp.Payload))
	}

	outputType := gjson.GetBytes(resp.Payload, "response.output.0.type").String()
	if outputType != "function_call" {
		t.Fatalf("expected output type %q, got %q", "function_call", outputType)
	}

	funcName := gjson.GetBytes(resp.Payload, "response.output.0.name").String()
	if funcName != "get_weather" {
		t.Fatalf("expected function name %q, got %q", "get_weather", funcName)
	}
}
