package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildCodexWebsocketRequestBodyDropsOrphanCustomToolCallFromFullTranscript(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"message","id":"msg-1","role":"user","content":"delegate"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-agent-1","name":"spawn_agent","input":"{}"},{"type":"message","id":"msg-2","role":"user","content":"start"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input length = %d, want 2 after dropping orphan custom tool call: %s", len(input), wsReqBody)
	}
	if input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected full transcript after sanitizing orphan custom tool call: %s", wsReqBody)
	}
}

func TestBuildCodexWebsocketRequestBodyKeepsMatchedCustomToolPair(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-agent-1","name":"spawn_agent","input":"{}"},{"type":"custom_tool_call_output","id":"cto-1","call_id":"call-agent-1","output":"started"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 2 {
		t.Fatalf("input length = %d, want matched custom tool pair: %s", len(input), wsReqBody)
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[1].Get("type").String() != "custom_tool_call_output" {
		t.Fatalf("matched custom tool pair was not preserved: %s", wsReqBody)
	}
}

func TestBuildCodexWebsocketRequestBodyKeepsIncrementalCustomToolOutput(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"custom_tool_call_output","id":"cto-1","call_id":"call-agent-1","output":"started"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 1 || input[0].Get("type").String() != "custom_tool_call_output" {
		t.Fatalf("incremental custom tool output must be preserved: %s", wsReqBody)
	}
}

func TestBuildCodexWebsocketRequestBodyDropsOrphanFunctionCallFromFullIncrementalTranscript(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1","role":"assistant","content":"delegate"},{"type":"function_call","id":"fc-1","call_id":"call-agent-1","name":"spawn_agent","arguments":"{}"},{"type":"message","id":"msg-2","role":"user","content":"start"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if gjson.GetBytes(wsReqBody, "previous_response_id").Exists() {
		t.Fatalf("full transcript should not retain previous_response_id: %s", wsReqBody)
	}
	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 2 || input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "msg-2" {
		t.Fatalf("orphan function call was not removed from full transcript: %s", wsReqBody)
	}
}

func TestBuildCodexWebsocketRequestBodyKeepsMatchedFunctionPairInFullIncrementalTranscript(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"function_call","id":"fc-1","call_id":"call-agent-1","name":"spawn_agent","arguments":"{}"},{"type":"function_call_output","id":"fco-1","call_id":"call-agent-1","output":"started"},{"type":"message","id":"msg-1","role":"user","content":"continue"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if gjson.GetBytes(wsReqBody, "previous_response_id").Exists() {
		t.Fatalf("full transcript should not retain previous_response_id: %s", wsReqBody)
	}
	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 3 || input[0].Get("type").String() != "function_call" || input[1].Get("type").String() != "function_call_output" {
		t.Fatalf("matched function pair was not preserved: %s", wsReqBody)
	}
}

func TestBuildCodexWebsocketRequestBodyKeepsPrewarmPendingCustomToolCall(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","generate":false,"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-agent-1","name":"spawn_agent","input":"{}"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	input := gjson.GetBytes(wsReqBody, "input").Array()
	if len(input) != 1 || input[0].Get("type").String() != "custom_tool_call" {
		t.Fatalf("prewarm pending custom tool call must be preserved: %s", wsReqBody)
	}
}
