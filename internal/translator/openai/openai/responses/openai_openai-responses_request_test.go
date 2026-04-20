package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AggregatesConsecutiveFunctionCalls(t *testing.T) {
	input := []byte(`{
		"model":"test-model",
		"input":[
			{"type":"function_call","call_id":"call1","name":"tool_a","arguments":"{\"x\":1}"},
			{"type":"function_call","call_id":"call2","name":"tool_b","arguments":"{\"y\":2}"},
			{"type":"function_call_output","call_id":"call1","output":"result1"},
			{"type":"function_call_output","call_id":"call2","output":"result2"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", input, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages len = %d, want 3. messages=%s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}

	if role := messages[0].Get("role").String(); role != "assistant" {
		t.Fatalf("messages[0].role = %q, want assistant", role)
	}
	toolCalls := messages[0].Get("tool_calls").Array()
	if len(toolCalls) != 2 {
		t.Fatalf("messages[0].tool_calls len = %d, want 2", len(toolCalls))
	}
	if got := toolCalls[0].Get("id").String(); got != "call1" {
		t.Fatalf("tool_calls[0].id = %q, want call1", got)
	}
	if got := toolCalls[1].Get("id").String(); got != "call2" {
		t.Fatalf("tool_calls[1].id = %q, want call2", got)
	}

	if role := messages[1].Get("role").String(); role != "tool" {
		t.Fatalf("messages[1].role = %q, want tool", role)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call1" {
		t.Fatalf("messages[1].tool_call_id = %q, want call1", got)
	}

	if role := messages[2].Get("role").String(); role != "tool" {
		t.Fatalf("messages[2].role = %q, want tool", role)
	}
	if got := messages[2].Get("tool_call_id").String(); got != "call2" {
		t.Fatalf("messages[2].tool_call_id = %q, want call2", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitsSeparatedToolTurns(t *testing.T) {
	input := []byte(`{
		"model":"test-model",
		"input":[
			{"type":"function_call","call_id":"call1","name":"tool_a","arguments":"{\"x\":1}"},
			{"type":"function_call_output","call_id":"call1","output":"result1"},
			{"type":"function_call","call_id":"call2","name":"tool_b","arguments":"{\"y\":2}"},
			{"type":"function_call_output","call_id":"call2","output":"result2"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", input, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages len = %d, want 4. messages=%s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}

	if role := messages[0].Get("role").String(); role != "assistant" {
		t.Fatalf("messages[0].role = %q, want assistant", role)
	}
	if got := messages[0].Get("tool_calls.0.id").String(); got != "call1" {
		t.Fatalf("messages[0].tool_calls[0].id = %q, want call1", got)
	}
	if role := messages[1].Get("role").String(); role != "tool" {
		t.Fatalf("messages[1].role = %q, want tool", role)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call1" {
		t.Fatalf("messages[1].tool_call_id = %q, want call1", got)
	}

	if role := messages[2].Get("role").String(); role != "assistant" {
		t.Fatalf("messages[2].role = %q, want assistant", role)
	}
	if got := messages[2].Get("tool_calls.0.id").String(); got != "call2" {
		t.Fatalf("messages[2].tool_calls[0].id = %q, want call2", got)
	}
	if role := messages[3].Get("role").String(); role != "tool" {
		t.Fatalf("messages[3].role = %q, want tool", role)
	}
	if got := messages[3].Get("tool_call_id").String(); got != "call2" {
		t.Fatalf("messages[3].tool_call_id = %q, want call2", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlushesTrailingFunctionCall(t *testing.T) {
	input := []byte(`{
		"model":"test-model",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","call_id":"call_tail","name":"tool_tail","arguments":"{}"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("test-model", input, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2. messages=%s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}

	if role := messages[0].Get("role").String(); role != "user" {
		t.Fatalf("messages[0].role = %q, want user", role)
	}
	if role := messages[1].Get("role").String(); role != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant", role)
	}
	toolCalls := messages[1].Get("tool_calls").Array()
	if len(toolCalls) != 1 {
		t.Fatalf("messages[1].tool_calls len = %d, want 1", len(toolCalls))
	}
	if got := toolCalls[0].Get("id").String(); got != "call_tail" {
		t.Fatalf("messages[1].tool_calls[0].id = %q, want call_tail", got)
	}
}
