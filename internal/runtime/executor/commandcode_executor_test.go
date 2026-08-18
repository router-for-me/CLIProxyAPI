package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCommandCodeExecutor_NonStream_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-cli-environment") != "production" {
			http.Error(w, "missing env header", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"Go channels are "}`)
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"typed conduits."}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":12,"outputTokens":8,"totalTokens":20}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{
		BaseURL: ts.URL,
	}

	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Provider: "commandcode",
		Attributes: map[string]string{
			"api_key": "test-api-key",
		},
	}

	reqJSON := []byte(`{
		"model": "deepseek/deepseek-v4-flash",
		"messages": [{"role": "user", "content": "Explain Go channels."}],
		"stream": false
	}`)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{
		Stream: false,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	root := gjson.ParseBytes(resp.Payload)
	content := root.Get("choices.0.message.content").String()
	if content != "Go channels are typed conduits." {
		t.Errorf("got content %q, want %q", content, "Go channels are typed conduits.")
	}
	if root.Get("choices.0.finish_reason").String() != "stop" {
		t.Errorf("got finish_reason %q, want 'stop'", root.Get("choices.0.finish_reason").String())
	}
	if root.Get("usage.total_tokens").Int() != 20 {
		t.Errorf("got total_tokens %d, want 20", root.Get("usage.total_tokens").Int())
	}
}

func TestCommandCodeExecutor_Stream_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"Hello "}`)
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"world!"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":5,"outputTokens":3,"totalTokens":8}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{
		BaseURL: ts.URL,
	}

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "test-key"},
	}

	reqJSON := []byte(`{"model": "deepseek/deepseek-v4-flash", "messages": [{"role": "user", "content": "hi"}], "stream": true}`)

	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{
		Stream:          true,
		OriginalRequest: reqJSON,
	})

	if err != nil {
		t.Fatalf("unexpected stream error: %v", err)
	}

	var fullText strings.Builder

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		raw := string(chunk.Payload)
		if gjson.Valid(raw) {
			c := gjson.Get(raw, "choices.0.delta.content").String()
			fullText.WriteString(c)
		}
	}

	if fullText.String() != "Hello world!" {
		t.Errorf("got stream content %q, want %q", fullText.String(), "Hello world!")
	}
}

func TestCommandCodeExecutor_ToolCall_NonStream(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"tool-input-start","id":"call_123","toolName":"get_weather"}`)
		fmt.Fprintln(w, `{"type":"tool-input-delta","id":"call_123","delta":"{\"city\":"}`)
		fmt.Fprintln(w, `{"type":"tool-input-delta","id":"call_123","delta":"\"Hangzhou\"}"}`)
		fmt.Fprintln(w, `{"type":"tool-input-end","id":"call_123"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":20,"outputTokens":15,"totalTokens":35}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	reqJSON := []byte(`{
		"model": "deepseek/deepseek-v4-flash",
		"messages": [{"role": "user", "content": "Weather in Hangzhou?"}],
		"tools": [{"type": "function", "function": {"name": "get_weather", "parameters": {"type": "object"}}}],
		"stream": false
	}`)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("tool call execution error: %v", err)
	}

	root := gjson.ParseBytes(resp.Payload)
	tc := root.Get("choices.0.message.tool_calls.0")
	if tc.Get("id").String() != "call_123" {
		t.Errorf("got tool_call id %q, want 'call_123'", tc.Get("id").String())
	}
	if tc.Get("function.name").String() != "get_weather" {
		t.Errorf("got tool_call name %q, want 'get_weather'", tc.Get("function.name").String())
	}
	args := tc.Get("function.arguments").String()
	if args != `{"city":"Hangzhou"}` {
		t.Errorf("got arguments %q, want %q", args, `{"city":"Hangzhou"}`)
	}
	if root.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Errorf("got finish_reason %q, want 'tool_calls'", root.Get("choices.0.finish_reason").String())
	}
}

func TestCommandCodeExecutor_LargePayload_NDJSON(t *testing.T) {
	largeArg := strings.Repeat("A", 128*1024) // 128 KiB
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"tool-input-start","id":"call_large","toolName":"large_fn"}`+"\n")
		escapedArg, _ := json.Marshal(largeArg)
		fmt.Fprintf(w, `{"type":"tool-input-delta","id":"call_large","delta":%s}`+"\n", string(escapedArg))
		fmt.Fprintf(w, `{"type":"finish-step","finishReason":"tool-calls","usage":{"totalTokens":100}}`+"\n")
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	reqJSON := []byte(`{"model": "deepseek/deepseek-v4-flash", "messages": [{"role": "user", "content": "run"}]}`)

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{})

	if err != nil {
		t.Fatalf("failed large payload read: %v", err)
	}

	root := gjson.ParseBytes(resp.Payload)
	retrieved := root.Get("choices.0.message.tool_calls.0.function.arguments").String()
	if !strings.Contains(retrieved, largeArg) {
		t.Errorf("large payload arguments truncated or corrupted (len=%d)", len(retrieved))
	}
}

func TestCommandCodeExecutor_UpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"upgrade_required"}}`, http.StatusPaymentRequired)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	reqJSON := []byte(`{"model": "deepseek/deepseek-v4-flash", "messages": [{"role": "user", "content": "hi"}]}`)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{})

	if err == nil {
		t.Fatal("expected error for 402 status, got nil")
	}
	if !strings.Contains(err.Error(), "upgrade_required") {
		t.Errorf("expected error message to contain 'upgrade_required', got: %v", err)
	}
}

func TestCommandCode_TranslatorRoundTrip(t *testing.T) {
	openAIReq := []byte(`{
		"model": "deepseek/deepseek-v4-flash",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Call foo"},
			{"role": "assistant", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "foo", "arguments": "{\"x\": 1}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result_1"}
		],
		"tools": [
			{"type": "function", "function": {"name": "foo", "description": "foo fn", "parameters": {"type": "object"}}}
		],
		"max_tokens": 1000,
		"temperature": 0.7
	}`)

	translated := helps.ConvertOpenAIToCommandCodeRequest("deepseek/deepseek-v4-flash", openAIReq, false)
	root := gjson.ParseBytes(translated)

	if root.Get("params.model").String() != "deepseek/deepseek-v4-flash" {
		t.Errorf("params.model = %q", root.Get("params.model").String())
	}
	if root.Get("params.system").String() != "You are a helpful assistant." {
		t.Errorf("params.system = %q", root.Get("params.system").String())
	}
	if root.Get("params.stream").Bool() != true {
		t.Errorf("params.stream must be forced to true")
	}

	msgs := root.Get("params.messages").Array()
	if len(msgs) != 3 { // user, assistant, tool
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[2].Get("role").String() != "tool" {
		t.Errorf("msg[2] role = %q, want 'tool'", msgs[2].Get("role").String())
	}
	if msgs[2].Get("content.0.toolName").String() != "foo" {
		t.Errorf("toolName resolution failed: got %q, want 'foo'", msgs[2].Get("content.0.toolName").String())
	}
}
