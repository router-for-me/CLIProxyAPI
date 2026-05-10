package responses

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func prettyJSONForTest(raw []byte) string {
	if !gjson.ValidBytes(raw) {
		return string(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergeConsecutiveFunctionCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"exec_command:0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"exec_command:1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"exec_command:0","output":"ok0"},
			{"type":"function_call_output","call_id":"exec_command:1","output":"ok1"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages should be an array")
	}
	if got := len(msgs.Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d", got, 2)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "exec_command:0" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != "exec_command:1" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "exec_command:1")
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "exec_command:0" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "exec_command:1" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "exec_command:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitFunctionCallsWhenInterrupted(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"next"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_a")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_calls.0.id").String(); got != "call_b" {
		t.Fatalf("messages.2.tool_calls.0.id = %q, want %q", got, "call_b")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilToolOutput(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_x","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},
			{"type":"message","role":"user","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_x","output":"ok"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 4 {
		t.Fatalf("messages count = %d, want %d", got, 4)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_x" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_x")
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "Approved command prefix saved" {
		t.Fatalf("messages.2.content = %q, want %q", got, "Approved command prefix saved")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "next" {
		t.Fatalf("messages.3.content = %q, want %q", got, "next")
	}
}
