package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_GroupsParallelFunctionCalls(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"model":"kimi-k2.6",
		"parallel_tool_calls":true,
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"read files"}]},
			{"type":"function_call","call_id":"read_a:0","name":"read_a","arguments":"{\"path\":\"a.txt\"}"},
			{"type":"function_call","call_id":"read_b:1","name":"read_b","arguments":"{\"path\":\"b.txt\"}"},
			{"type":"function_call_output","call_id":"read_a:0","output":"A"},
			{"type":"function_call_output","call_id":"read_b:1","output":"B"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages length = %d, want 4, raw = %s", len(messages), out)
	}

	if messages[1].Get("role").String() != "assistant" {
		t.Fatalf("messages.1.role = %q, want assistant", messages[1].Get("role").String())
	}

	toolCalls := messages[1].Get("tool_calls").Array()
	if len(toolCalls) != 2 {
		t.Fatalf("messages.1.tool_calls length = %d, want 2, raw = %s", len(toolCalls), messages[1].Get("tool_calls").Raw)
	}

	if got := toolCalls[0].Get("id").String(); got != "read_a:0" {
		t.Fatalf("tool call 0 id = %q, want %q", got, "read_a:0")
	}
	if got := toolCalls[1].Get("id").String(); got != "read_b:1" {
		t.Fatalf("tool call 1 id = %q, want %q", got, "read_b:1")
	}
	if got := toolCalls[0].Get("function.name").String(); got != "read_a" {
		t.Fatalf("tool call 0 name = %q, want %q", got, "read_a")
	}
	if got := toolCalls[1].Get("function.name").String(); got != "read_b" {
		t.Fatalf("tool call 1 name = %q, want %q", got, "read_b")
	}

	if got := messages[2].Get("tool_call_id").String(); got != "read_a:0" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "read_a:0")
	}
	if got := messages[3].Get("tool_call_id").String(); got != "read_b:1" {
		t.Fatalf("messages.3.tool_call_id = %q, want %q", got, "read_b:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesSingleFunctionCallFlow(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"model":"kimi-k2.6",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]},
			{"type":"function_call","call_id":"list_files:0","name":"list_files","arguments":"{}"},
			{"type":"function_call_output","call_id":"list_files:0","output":"[]"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3, raw = %s", len(messages), out)
	}

	toolCalls := messages[1].Get("tool_calls").Array()
	if len(toolCalls) != 1 {
		t.Fatalf("messages.1.tool_calls length = %d, want 1", len(toolCalls))
	}
	if got := toolCalls[0].Get("id").String(); got != "list_files:0" {
		t.Fatalf("tool call id = %q, want %q", got, "list_files:0")
	}
	if got := messages[2].Get("tool_call_id").String(); got != "list_files:0" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "list_files:0")
	}
}
