package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Issue #5551: Tool schema with 13-branch oneOf causes ChatGPT Codex upstream
// to abort silently. The executor must simplify the complex union without destroying
// property names, enum values, or sibling properties.
func TestCodexExecutorSimplifiesComplexOneOfToolSchema(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}

	// 13-branch oneOf reproduction payload from Issue #5551
	requestPayload := []byte(`{
		"model": "gpt-5.5",
		"messages": [{"role": "user", "content": "Reply with exactly: OK"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "t1",
				"description": "test tool",
				"parameters": {
					"type": "object",
					"properties": {
						"action": {
							"type": "string",
							"enum": ["p.list","m.list","s.list","s.create","s.send","s.fork","s.status","s.messages","sch.list","sch.create","sch.run","sch.delete","sch.toggle"],
							"oneOf": [
								{"const": "p.list", "description": "List projects"},
								{"const": "m.list", "description": "List models"},
								{"const": "s.list", "description": "List sessions"},
								{"const": "s.create", "description": "Create session"},
								{"const": "s.send", "description": "Send prompt"},
								{"const": "s.fork", "description": "Fork session"},
								{"const": "s.status", "description": "Session status"},
								{"const": "s.messages", "description": "Session messages"},
								{"const": "sch.list", "description": "List schedule"},
								{"const": "sch.create", "description": "Create schedule"},
								{"const": "sch.run", "description": "Run schedule"},
								{"const": "sch.delete", "description": "Delete schedule"},
								{"const": "sch.toggle", "description": "Toggle schedule"}
							],
							"description": "Action to perform"
						},
						"target_id": {
							"type": "string",
							"description": "Optional target"
						}
					},
					"required": ["action"]
				}
			}
		}]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	tools := gjson.GetBytes(gotBody, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in upstream payload, got: %s", gotBody)
	}

	// Find tool t1
	var t1Tool gjson.Result
	for _, tool := range tools {
		if tool.Get("name").String() == "t1" {
			t1Tool = tool
			break
		}
	}
	if !t1Tool.Exists() {
		t1Tool = tools[0]
	}

	// Complex oneOf must be removed from action property
	if t1Tool.Get("parameters.properties.action.oneOf").Exists() {
		t.Fatalf("expected complex oneOf to be removed from tool parameters, got: %s", t1Tool.Get("parameters").Raw)
	}
	// Action type and enum must be preserved
	if t1Tool.Get("parameters.properties.action.type").String() != "string" {
		t.Fatalf("expected action.type = string, got: %s", t1Tool.Get("parameters.properties.action.type").String())
	}
	if len(t1Tool.Get("parameters.properties.action.enum").Array()) != 13 {
		t.Fatalf("expected action.enum to retain all 13 items")
	}
	// Sibling property target_id must be preserved
	if t1Tool.Get("parameters.properties.target_id.type").String() != "string" {
		t.Fatalf("sibling property target_id was dropped")
	}
	// Required field must be preserved
	if t1Tool.Get("parameters.required.0").String() != "action" {
		t.Fatalf("required list was dropped")
	}
}

// Issue #5551: If ChatGPT Codex upstream terminates immediately with response.incomplete,
// reason: "max_output_tokens", and output_tokens: 0 (no output produced), it must be treated
// as an upstream failure rather than a silent successful completion.
func TestCodexExecutorExecuteStream_ZeroTokenIncompleteResponseIsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		// Error returned before streaming is also valid failure
		return
	}

	// If streaming started, chunks must surface an error rather than silent completion
	sawError := false
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Fatalf("expected stream to fail on zero-token response.incomplete, but it completed without error")
	}
}

// Issue #5551: If a stream emitted output text deltas before response.incomplete,
// it produced partial output and must NOT be treated as a zero-token empty failure.
func TestCodexExecutorExecuteStream_PartialDeltasIncompleteResponseIsSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello world\"}\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected initial error: %v", err)
	}

	sawDelta := false
	sawError := false
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
		if len(chunk.Payload) > 0 {
			sawDelta = true
		}
	}
	if sawError {
		t.Fatalf("expected partial output stream to finish without failure, but got chunk error")
	}
	if !sawDelta {
		t.Fatalf("expected to receive output delta")
	}
}

// Issue #5551: Delta seen during the buffering phase must be remembered when transitioning
// to the streaming goroutine so zero-token response.incomplete is not falsely flagged as empty.
func TestCodexExecutorExecuteStream_BufferingPartialDeltasIncompleteResponseIsSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Buffered chunk\"}\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{
		Codex: config.CodexConfig{
			StreamBootstrapBuffering: true,
		},
	})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected initial error: %v", err)
	}

	sawDelta := false
	sawError := false
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
		if len(chunk.Payload) > 0 {
			sawDelta = true
		}
	}
	if sawError {
		t.Fatalf("expected buffered partial output stream to finish without failure, but got chunk error")
	}
	if !sawDelta {
		t.Fatalf("expected to receive buffered output delta")
	}
}

// Issue #5551: An empty string delta ("") before response.incomplete must NOT be treated as
// meaningful output, and the stream must correctly surface the zero-token failure.
func TestCodexExecutorExecuteStream_EmptyDeltaDoesNotBypassZeroTokenFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"\"}\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		return
	}

	sawError := false
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			sawError = true
			break
		}
	}
	if !sawError {
		t.Fatalf("expected stream with empty delta to fail on zero-token response.incomplete, but it succeeded")
	}
}
