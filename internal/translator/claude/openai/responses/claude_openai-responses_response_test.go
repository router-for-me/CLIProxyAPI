package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseClaudeResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	var event string
	var data string
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}

	return event, gjson.Parse(data)
}

func TestConvertClaudeResponseToOpenAIResponses_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_123"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				reasoningDone = data
			}
		case "response.completed":
			completed = data
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != signature {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoningDone.Get("item.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("reasoning summary text = %q", got)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("completed reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_ToolSearchUseBecomesToolSearchCall(t *testing.T) {
	originalRequest := []byte(`{"model":"claude-test","tools":[{"type":"tool_search"}]}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_tool_search","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_search","name":"ToolSearch","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"task\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var added gjson.Result
	var done gjson.Result
	var completed gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", originalRequest, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "tool_search_call" {
					added = data
				}
			case "response.output_item.done":
				if data.Get("item.type").String() == "tool_search_call" {
					done = data
				}
			case "response.completed":
				completed = data
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				t.Fatalf("ToolSearch should not emit %s: %s", event, string(output))
			}
		}
	}

	if !added.Exists() {
		t.Fatal("expected tool_search_call output_item.added event")
	}
	if got := added.Get("item.call_id").String(); got != "toolu_search" {
		t.Fatalf("added call_id = %q, want toolu_search", got)
	}
	if got := added.Get("item.execution").String(); got != "server" {
		t.Fatalf("added execution = %q, want server", got)
	}
	if added.Get("item.name").Exists() {
		t.Fatalf("tool_search_call should not include function name: %s", added.Raw)
	}
	if !done.Exists() {
		t.Fatal("expected tool_search_call output_item.done event")
	}
	if got := done.Get("item.arguments.query").String(); got != "task" {
		t.Fatalf("done arguments.query = %q, want task", got)
	}
	if got := completed.Get("response.output.0.type").String(); got != "tool_search_call" {
		t.Fatalf("completed output type = %q, want tool_search_call. Completed: %s", got, completed.Raw)
	}
	if got := completed.Get("response.output.0.arguments.query").String(); got != "task" {
		t.Fatalf("completed arguments.query = %q, want task", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_nonstream"
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"nonstream reasoning"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.encrypted_content").String(); got != signature {
		t.Fatalf("non-stream reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := root.Get("output.0.summary.0.text").String(); got != "nonstream reasoning" {
		t.Fatalf("non-stream reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_CustomToolSearchUse(t *testing.T) {
	originalRequest := []byte(`{"model":"claude-test","tools":[{"type":"tool_search","name":"CustomToolSearch","execution":"client"}]}`)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_custom_tool_search","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_custom","name":"CustomToolSearch","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"task\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil)
	root := gjson.ParseBytes(out)
	item := root.Get("output.0")

	if got := item.Get("type").String(); got != "tool_search_call" {
		t.Fatalf("output type = %q, want tool_search_call. Output: %s", got, string(out))
	}
	if got := item.Get("call_id").String(); got != "toolu_custom" {
		t.Fatalf("call_id = %q, want toolu_custom. Output: %s", got, string(out))
	}
	if got := item.Get("execution").String(); got != "client" {
		t.Fatalf("execution = %q, want client. Output: %s", got, string(out))
	}
	if got := item.Get("arguments.query").String(); got != "task" {
		t.Fatalf("arguments.query = %q, want task. Output: %s", got, string(out))
	}
	if item.Get("name").Exists() {
		t.Fatalf("tool_search_call should not include function name. Output: %s", string(out))
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_OrdinaryToolUseRemainsFunctionCall(t *testing.T) {
	originalRequest := []byte(`{"model":"claude-test","tools":[{"type":"tool_search"},{"type":"function","name":"Lookup","parameters":{"type":"object","properties":{}}}]}`)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_function","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_lookup","name":"Lookup","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", originalRequest, nil, raw, nil)
	root := gjson.ParseBytes(out)
	item := root.Get("output.0")

	if got := item.Get("type").String(); got != "function_call" {
		t.Fatalf("output type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := item.Get("name").String(); got != "Lookup" {
		t.Fatalf("function name = %q, want Lookup. Output: %s", got, string(out))
	}
	if got := item.Get("call_id").String(); got != "toolu_lookup" {
		t.Fatalf("call_id = %q, want toolu_lookup. Output: %s", got, string(out))
	}
}
