package claude

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsResponseToClaudeStream_LengthStopReasonMapsToMaxTokens(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"edit","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.go\",\"content\":\"partial"}}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"incomplete","stop_reason":"length","usage":{"total_input_tokens":2,"total_output_tokens":3}}}`),
	}
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}
	delta := findClaudeEventPayload(out, "message_delta")
	if got := gjson.GetBytes(delta, "delta.stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens. Payload: %s", got, delta)
	}
}

func TestConvertInteractionsResponseToClaudeStream_ToolCallStillToolUse(t *testing.T) {
	var param any
	chunks := [][]byte{
		[]byte(`data: {"event_type":"interaction.created","interaction":{"id":"i1","model":"devin/swe-2"}}`),
		[]byte(`data: {"event_type":"step.start","index":0,"step":{"type":"function_call","id":"call_1","name":"edit","arguments":{}}}`),
		[]byte(`data: {"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a.go\"}"}}`),
		[]byte(`data: {"event_type":"interaction.completed","interaction":{"id":"i1","status":"completed","stop_reason":"tool_calls"}}`),
	}
	var out [][]byte
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "devin/swe-2", nil, nil, chunk, &param)...)
	}
	delta := findClaudeEventPayload(out, "message_delta")
	if got := gjson.GetBytes(delta, "delta.stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Payload: %s", got, delta)
	}
}

func TestConvertInteractionsResponseToClaudeNonStream_LengthStopReason(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"devin/swe-2","status":"incomplete","stop_reason":"length","steps":[{"type":"model_output","content":[{"type":"text","text":"partial"}]}]}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "devin/swe-2", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens. Output: %s", got, out)
	}
}
