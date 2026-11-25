package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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

	tools := ojson.GetBytes(gotBody, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in upstream payload, got: %s", gotBody)
	}

	// Find tool t1
	var t1Tool ojson.Result
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

func TestCodexExecutor_DoesNotNormalizeToolIntegerTypesForCodexUserAgent(t *testing.T) {
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
			"api_key":  "test",
			"base_url": server.URL,
		},
	}

	requestPayload := []byte(`{
		"model": "gpt-5.5",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"cmd": {"type": "string"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"parameters": {
					"type": "object",
					"properties": {
						"session_id": {"type": "number"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "sleep",
				"parameters": {
					"type": "object",
					"properties": {
						"duration_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait_agent",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"max_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "tool_search",
				"parameters": {
					"type": "object",
					"properties": {
						"limit": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "test_sync_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"sleep_before_ms": {"type": "number"},
						"sleep_after_ms": {"type": "number"},
						"participants": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "unrelated_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"unrelated_num": {"type": "number"}
					}
				}
			},
			{
				"type": "namespace",
				"name": "collaboration",
				"tools": [
					{
						"type": "function",
						"name": "wait_agent",
						"parameters": {
							"type": "object",
							"properties": {
								"timeout_ms": {"type": "number"}
							}
						}
					}
				]
			}
		],
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": "number"}
							}
						}
					},
					{
						"type": "namespace",
						"name": "collaboration",
						"tools": [
							{
								"type": "function",
								"name": "wait_agent",
								"parameters": {
									"type": "object",
									"properties": {
										"timeout_ms": {"type": "number"}
									}
								}
							}
						]
					}
				]
			}
		]
	}`)

	// 1. With non-Codex User-Agent, tool types must remain unchanged (number).
	_, errExecuteNonCodex := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
		Stream:       false,
	})
	if errExecuteNonCodex != nil {
		t.Fatalf("Execute(non-codex) error = %v", errExecuteNonCodex)
	}

	toolMapNonCodex := make(map[string]ojson.Result)
	for _, tool := range ojson.GetBytes(gotBody, "tools").Array() {
		toolMapNonCodex[tool.Get("name").String()] = tool
	}
	if gotType := toolMapNonCodex["exec_command"].Get("parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("expected non-Codex UA to preserve number, got %q", gotType)
	}

	// 2. With Codex User-Agent, tool parameter number types must NOT be rewritten to integer for Codex executor (Issue #6244).
	gotBody = nil
	_, errExecuteCodex := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1.0"}},
		Stream:       false,
	})
	if errExecuteCodex != nil {
		t.Fatalf("Execute(codex) error = %v", errExecuteCodex)
	}

	toolMap := make(map[string]ojson.Result)
	for _, tool := range ojson.GetBytes(gotBody, "tools").Array() {
		toolMap[tool.Get("name").String()] = tool
	}

	expectedFields := map[string][]string{
		"exec_command":   {"yield_time_ms", "max_output_tokens", "timeout_ms"},
		"write_stdin":    {"session_id", "yield_time_ms", "max_output_tokens"},
		"sleep":          {"duration_ms"},
		"wait_agent":     {"timeout_ms"},
		"wait":           {"yield_time_ms", "max_tokens"},
		"tool_search":    {"limit"},
		"test_sync_tool": {"sleep_before_ms", "sleep_after_ms", "participants", "timeout_ms"},
	}

	for toolName, fields := range expectedFields {
		tool, ok := toolMap[toolName]
		if !ok {
			t.Fatalf("expected tool %q in upstream payload", toolName)
		}
		for _, field := range fields {
			gotType := tool.Get("parameters.properties." + field + ".type").String()
			if gotType != "number" {
				t.Fatalf("tool %s property %s type = %q, want number (should not normalize for Codex executor)", toolName, field, gotType)
			}
		}
	}

	// Unrelated tool must retain number
	unrelated := toolMap["unrelated_tool"]
	if gotType := unrelated.Get("parameters.properties.unrelated_num.type").String(); gotType != "number" {
		t.Fatalf("unrelated_tool property type = %q, want number", gotType)
	}

	// Namespace collaboration tool wait_agent must retain number (Issue #6244)
	if gotType := ojson.GetBytes(gotBody, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); gotType != "number" {
		t.Fatalf("collaboration.wait_agent timeout_ms type = %q, want number", gotType)
	}

	// input[].additional_tools must retain number
	if gotType := ojson.GetBytes(gotBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("input additional_tools yield_time_ms type = %q, want number", gotType)
	}
	if gotType := ojson.GetBytes(gotBody, "input.#(type==\"additional_tools\").tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); gotType != "number" {
		t.Fatalf("input additional_tools collaboration.wait_agent timeout_ms type = %q, want number", gotType)
	}

	// 3. HTTP Stream: must also retain number
	gotBody = nil
	streamRes, errStream := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1.0"}},
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream(codex) error = %v", errStream)
	}
	for range streamRes.Chunks {
	}
	if gotType := ojson.GetBytes(gotBody, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP stream exec_command yield_time_ms type = %q, want number", gotType)
	}
	if gotType := ojson.GetBytes(gotBody, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP stream collaboration.wait_agent timeout_ms type = %q, want number", gotType)
	}
	if gotType := ojson.GetBytes(gotBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP stream input additional_tools yield_time_ms type = %q, want number", gotType)
	}

	// 4. HTTP Compact (/responses/compact): must also retain number
	gotBody = nil
	_, errCompact := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1.0"}},
		Alt:          "responses/compact",
	})
	if errCompact != nil {
		t.Fatalf("ExecuteCompact(codex) error = %v", errCompact)
	}
	if gotType := ojson.GetBytes(gotBody, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP compact exec_command yield_time_ms type = %q, want number", gotType)
	}
	if gotType := ojson.GetBytes(gotBody, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP compact collaboration.wait_agent timeout_ms type = %q, want number", gotType)
	}
	if gotType := ojson.GetBytes(gotBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); gotType != "number" {
		t.Fatalf("HTTP compact input additional_tools yield_time_ms type = %q, want number", gotType)
	}
}

func TestCodexWebsocketsExecutor_DoesNotNormalizeToolIntegerTypesForCodexUserAgent(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}

	requestPayload := []byte(`{
		"model": "gpt-5.6-luna",
		"input": [
			{"type":"message","role":"user","content":"hi"},
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": "number"}
							}
						}
					},
					{
						"type": "namespace",
						"name": "collaboration",
						"tools": [
							{
								"type": "function",
								"name": "wait_agent",
								"parameters": {
									"type": "object",
									"properties": {
										"timeout_ms": {"type": "number"}
									}
								}
							}
						]
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "namespace",
				"name": "collaboration",
				"tools": [
					{
						"type": "function",
						"name": "wait_agent",
						"parameters": {
							"type": "object",
							"properties": {
								"timeout_ms": {"type": "number"}
							}
						}
					}
				]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: requestPayload,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1.0"}},
	}

	result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	streamComplete := false
	for !streamComplete {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				streamComplete = true
				continue
			}
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for websocket stream completion")
		}
	}

	select {
	case payload := <-capturedPayload:
		if got := ojson.GetBytes(payload, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("WS stream exec_command yield_time_ms type = %q, want number", got)
		}
		if got := ojson.GetBytes(payload, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); got != "number" {
			t.Fatalf("WS stream collaboration.wait_agent timeout_ms type = %q, want number", got)
		}
		if got := ojson.GetBytes(payload, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("WS stream input additional_tools yield_time_ms type = %q, want number", got)
		}
		if got := ojson.GetBytes(payload, "input.#(type==\"additional_tools\").tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); got != "number" {
			t.Fatalf("WS stream input additional_tools collaboration.wait_agent timeout_ms type = %q, want number", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}

	// Also test WS non-stream Execute
	_, errExecWS := exec.Execute(context.Background(), auth, req, opts)
	if errExecWS != nil {
		t.Fatalf("WS Execute() error = %v", errExecWS)
	}
	select {
	case payload := <-capturedPayload:
		if got := ojson.GetBytes(payload, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("WS non-stream exec_command yield_time_ms type = %q, want number", got)
		}
		if got := ojson.GetBytes(payload, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); got != "number" {
			t.Fatalf("WS non-stream collaboration.wait_agent timeout_ms type = %q, want number", got)
		}
		if got := ojson.GetBytes(payload, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("WS non-stream input additional_tools yield_time_ms type = %q, want number", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket non-stream payload")
	}
}

func TestCodexWebsocketsExecutor_UpgradeFallbackToHTTP_PreservesNumber(t *testing.T) {
	var httpCapturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"websocket unavailable"}}`))
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read HTTP fallback body: %v", errRead)
		}
		httpCapturedBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fallback\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "sk-test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}

	requestPayload := []byte(`{
		"model": "gpt-5.6-luna",
		"input": [
			{"type":"message","role":"user","content":"hi"},
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": "number"}
							}
						}
					}
				]
			}
		],
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "namespace",
				"name": "collaboration",
				"tools": [
					{
						"type": "function",
						"name": "wait_agent",
						"parameters": {
							"type": "object",
							"properties": {
								"timeout_ms": {"type": "number"}
							}
						}
					}
				]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: requestPayload,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.1.0"}},
	}

	// 1. Stream fallback
	httpCapturedBody = nil
	streamRes, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream fallback error = %v", errStream)
	}
	for range streamRes.Chunks {
	}
	if got := ojson.GetBytes(httpCapturedBody, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); got != "number" {
		t.Fatalf("stream fallback tools exec_command yield_time_ms type = %q, want number", got)
	}
	if got := ojson.GetBytes(httpCapturedBody, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); got != "number" {
		t.Fatalf("stream fallback collaboration.wait_agent timeout_ms type = %q, want number", got)
	}
	if got := ojson.GetBytes(httpCapturedBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
		t.Fatalf("stream fallback input additional_tools yield_time_ms type = %q, want number", got)
	}

	// 2. Non-stream fallback
	httpCapturedBody = nil
	_, errNonStream := exec.Execute(context.Background(), auth, req, opts)
	if errNonStream != nil {
		t.Fatalf("Execute fallback error = %v", errNonStream)
	}
	if got := ojson.GetBytes(httpCapturedBody, "tools.#(name==\"exec_command\").parameters.properties.yield_time_ms.type").String(); got != "number" {
		t.Fatalf("non-stream fallback tools exec_command yield_time_ms type = %q, want number", got)
	}
	if got := ojson.GetBytes(httpCapturedBody, "tools.#(name==\"collaboration\").tools.0.parameters.properties.timeout_ms.type").String(); got != "number" {
		t.Fatalf("non-stream fallback collaboration.wait_agent timeout_ms type = %q, want number", got)
	}
	if got := ojson.GetBytes(httpCapturedBody, "input.#(type==\"additional_tools\").tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
		t.Fatalf("non-stream fallback input additional_tools yield_time_ms type = %q, want number", got)
	}
}

func TestOpenAICompatExecutor_NormalizesToolIntegerTypesForCodexUserAgent_NonCodexTarget(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	requestPayload := []byte(`{
		"model": "test-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"},
							"timeout_ms": {"type": "number"}
						}
					}
				}
			}
		]
	}`)

	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Headers:      http.Header{"User-Agent": []string{"codex-tui/0.154.0"}},
		Stream:       false,
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	if got := ojson.GetBytes(gotBody, "tools.0.function.parameters.properties.yield_time_ms.type").String(); got != "integer" {
		t.Fatalf("expected non-Codex executor to normalize yield_time_ms to integer, got: %s (body=%s)", got, string(gotBody))
	}
	if got := ojson.GetBytes(gotBody, "tools.0.function.parameters.properties.timeout_ms.type").String(); got != "integer" {
		t.Fatalf("expected non-Codex executor to normalize timeout_ms to integer, got: %s (body=%s)", got, string(gotBody))
	}
}

// Issue #7690: Codex Responses rejects function tool schemas containing "uniqueItems"
// on gpt-6.2-astra when "strict" is omitted or true. All Codex HTTP and WebSocket
// execution paths must strip uniqueItems while preserving strict semantics and literal
// properties named "uniqueItems".
func TestCodexExecutor_StripsUniqueItemsFromToolSchemas(t *testing.T) {
	requestPayload := []byte(`{
		"model": "gpt-6.2-astra",
		"instructions": "Reply with ok",
		"input": [{"role": "user", "content": [{"type": "input_text", "text": "Say ok"}]}],
		"tools": [
			{
				"type": "function",
				"name": "ask_user",
				"description": "Ask the user a structured question.",
				"parameters": {
					"type": "object",
					"properties": {
						"question": {"type": "string"},
						"options": {
							"type": "array",
							"items": {"type": "string"},
							"uniqueItems": true
						}
					},
					"required": ["question"]
				}
			},
			{
				"type": "function",
				"name": "strict_tags",
				"strict": true,
				"parameters": {
					"type": "object",
					"properties": {
						"tags": {
							"type": "array",
							"items": {"type": "string"},
							"uniqueItems": false
						},
						"uniqueItems": {"type": "boolean"}
					},
					"required": ["tags", "uniqueItems"],
					"additionalProperties": false
				}
			}
		],
		"stream": true,
		"store": false
	}`)

	assertPayload := func(t *testing.T, mode string, body []byte) {
		t.Helper()
		askTool := ojson.GetBytes(body, `tools.#(name=="ask_user")`)
		if !askTool.Exists() {
			t.Fatalf("%s: ask_user tool missing in body: %s", mode, string(body))
		}
		if askTool.Get("strict").Exists() {
			t.Fatalf("%s: expected omitted strict on ask_user to remain omitted, got %s", mode, askTool.Get("strict").Raw)
		}
		if askTool.Get("parameters.properties.options.uniqueItems").Exists() {
			t.Fatalf("%s: expected ask_user options.uniqueItems to be stripped, got %s", mode, askTool.Get("parameters.properties.options").Raw)
		}
		strictTool := ojson.GetBytes(body, `tools.#(name=="strict_tags")`)
		if !strictTool.Exists() {
			t.Fatalf("%s: strict_tags tool missing in body: %s", mode, string(body))
		}
		if strictTool.Get("strict").Type != ojson.True {
			t.Fatalf("%s: expected strict=true preserved on strict_tags, got %s", mode, strictTool.Get("strict").Raw)
		}
		if strictTool.Get("parameters.properties.tags.uniqueItems").Exists() {
			t.Fatalf("%s: expected strict_tags tags.uniqueItems to be stripped, got %s", mode, strictTool.Get("parameters.properties.tags").Raw)
		}
		if gotType := strictTool.Get("parameters.properties.uniqueItems.type").String(); gotType != "boolean" {
			t.Fatalf("%s: expected literal property uniqueItems preserved, got %q", mode, gotType)
		}
	}

	t.Run("http", func(t *testing.T) {
		var gotBody []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Errorf("read request body: %v", errRead)
				http.Error(w, errRead.Error(), http.StatusInternalServerError)
				return
			}
			gotBody = bytes.Clone(body)
			if r.URL.Path == "/responses/compact" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","output":[]}`))
				return
			}
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
		req := cliproxyexecutor.Request{Model: "gpt-6.2-astra", Payload: requestPayload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

		gotBody = nil
		if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
			t.Fatalf("Execute() error = %v", errExec)
		}
		assertPayload(t, "http execute", gotBody)

		gotBody = nil
		streamRes, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for range streamRes.Chunks {
		}
		assertPayload(t, "http stream", gotBody)

		gotBody = nil
		compactOpts := opts
		compactOpts.Alt = "responses/compact"
		if _, errCompact := executor.Execute(context.Background(), auth, req, compactOpts); errCompact != nil {
			t.Fatalf("Execute(compact) error = %v", errCompact)
		}
		assertPayload(t, "http compact", gotBody)
	})

	t.Run("websocket", func(t *testing.T) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		capturedPayload := make(chan []byte, 2)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, errUpgrade := upgrader.Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade websocket: %v", errUpgrade)
				return
			}
			defer func() { _ = conn.Close() }()

			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read upstream websocket message: %v", errRead)
				return
			}
			capturedPayload <- bytes.Clone(payload)

			completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				t.Errorf("write completed websocket message: %v", errWrite)
			}
		}))
		defer server.Close()

		exec := NewCodexWebsocketsExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{
			Provider: "codex",
			Attributes: map[string]string{
				"api_key":   "sk-test",
				"base_url":  server.URL,
				"plan_type": "pro",
			},
		}
		req := cliproxyexecutor.Request{Model: "gpt-6.2-astra", Payload: requestPayload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}

		streamRes, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("WS ExecuteStream() error = %v", errStream)
		}
		for chunk := range streamRes.Chunks {
			if chunk.Err != nil {
				t.Fatalf("WS stream chunk error = %v", chunk.Err)
			}
		}
		select {
		case payload := <-capturedPayload:
			assertPayload(t, "ws stream", payload)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for ws stream payload")
		}

		if _, errExec := exec.Execute(context.Background(), auth, req, opts); errExec != nil {
			t.Fatalf("WS Execute() error = %v", errExec)
		}
		select {
		case payload := <-capturedPayload:
			assertPayload(t, "ws execute", payload)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for ws execute payload")
		}
	})
}
