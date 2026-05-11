package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestRepairCodexResponsesToolHistoryDropsInvalidToolHistory(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"input": [
			{"type":"message","role":"user","content":[]},
			{"type":"message","role":"user","content":[
				{"type":"unsupported","value":1},
				{"type":"input_text","text":"run"}
			]},
			{"type":"function_call","call_id":"call_ok","name":"read_file","arguments":{"path":"README.md"}},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"},
			{"type":"function_call","call_id":"call_ok","name":"read_file","arguments":"{}"},
			{"type":"function_call","call_id":"call_no_output","name":"grep","arguments":"{}"},
			{"type":"function_call","call_id":"call_missing_name","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_orphan","output":"orphan"},
			{"type":"function_call_output","call_id":"call_ok","output":"duplicate"}
		]
	}`)

	out := repairCodexResponsesToolHistory(body)
	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3: %s", len(input), string(out))
	}
	if got := input[0].Get("type").String(); got != "message" {
		t.Fatalf("item 0 type = %q, want message: %s", got, string(out))
	}
	if got := input[0].Get("content.#").Int(); got != 1 {
		t.Fatalf("cleaned message content length = %d, want 1: %s", got, string(out))
	}
	if got := input[1].Get("type").String(); got != "function_call" {
		t.Fatalf("item 1 type = %q, want function_call: %s", got, string(out))
	}
	if got := input[1].Get("arguments").String(); got != `{"path":"README.md"}` {
		t.Fatalf("function_call arguments = %q, want serialized object: %s", got, string(out))
	}
	if got := input[2].Get("type").String(); got != "function_call_output" {
		t.Fatalf("item 2 type = %q, want function_call_output: %s", got, string(out))
	}
	if gjson.GetBytes(out, `input.#(call_id=="call_orphan")`).Exists() {
		t.Fatalf("orphan output should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, `input.#(call_id=="call_no_output")`).Exists() {
		t.Fatalf("unanswered call should be removed: %s", string(out))
	}
}

func TestRepairCodexResponsesToolHistoryKeepsPreviousResponseOutput(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"previous_response_id": "resp_1",
		"input": [
			{"type":"function_call_output","call_id":"call_prev","output":"ok"}
		]
	}`)

	out := repairCodexResponsesToolHistory(body)
	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 1 {
		t.Fatalf("input length = %d, want 1: %s", len(input), string(out))
	}
	if got := input[0].Get("call_id").String(); got != "call_prev" {
		t.Fatalf("call_id = %q, want call_prev: %s", got, string(out))
	}
}
