package helps

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestParseClaudeUsageStopReason(t *testing.T) {
	for wire, want := range map[string]usage.StopReason{
		`"end_turn"`:                      usage.StopReasonEndTurn,
		`"stop_sequence"`:                 usage.StopReasonEndTurn,
		`"tool_use"`:                      usage.StopReasonToolUse,
		`"max_tokens"`:                    usage.StopReasonMaxTokens,
		`"model_context_window_exceeded"`: usage.StopReasonMaxTokens,
		`"refusal"`:                       usage.StopReasonRefusal,
		`"pause_turn"`:                    usage.StopReasonPause,
		`"a_future_reason"`:               "",
		`null`:                            "",
	} {
		body := fmt.Sprintf(`{"type":"message","content":[],"stop_reason":%s,"usage":{"input_tokens":10,"output_tokens":5}}`, wire)
		if got := ParseClaudeUsage([]byte(body)).StopReason; got != want {
			t.Errorf("stop_reason %s: got %q, want %q", wire, got, want)
		}
	}
}

func TestStreamUsageBufferKeepsClaudeStopReason(t *testing.T) {
	var buffer StreamUsageBuffer
	for _, line := range []string{
		`data: {"type":"message_start","message":{"model":"claude-opus-5-5","stop_reason":null,"usage":{"input_tokens":100,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":40}}`,
		`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":42}}`,
		`data: {"type":"message_stop"}`,
	} {
		buffer.ObserveClaudeStream([]byte(line))
	}
	detail, ok := buffer.Detail()
	if !ok || detail.StopReason != usage.StopReasonToolUse || detail.InputTokens != 100 || detail.OutputTokens != 42 {
		t.Fatalf("detail = %+v (ok %v), want tool_use with 100 input and 42 output tokens", detail, ok)
	}
}

func TestParseCodexUsageStopReason(t *testing.T) {
	const usageJSON = `{"input_tokens":10,"output_tokens":5,"total_tokens":15}`
	message := `{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}`
	functionCall := `{"type":"function_call","id":"fc_1","call_id":"call_1","status":"completed","name":"shell","arguments":"{}"}`
	customToolCall := `{"type":"custom_tool_call","id":"ctc_1","call_id":"call_2","status":"completed","name":"apply_patch","input":"*** Begin Patch"}`
	webSearch := `{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"go"}}`
	terminal := func(eventType, status, extra, output string) string {
		return fmt.Sprintf(`{"type":%q,"response":{"id":"resp_1","status":%q%s,"output":[%s],"usage":%s}}`, eventType, status, extra, output, usageJSON)
	}
	tests := []struct {
		name      string
		payload   string
		collected map[int64][]byte
		want      usage.StopReason
	}{
		{"completed with a message", terminal("response.completed", "completed", "", message), nil, usage.StopReasonEndTurn},
		{"completed with a function call", terminal("response.completed", "completed", "", message+","+functionCall), nil, usage.StopReasonToolUse},
		{"completed with a custom tool call", terminal("response.completed", "completed", "", customToolCall), nil, usage.StopReasonToolUse},
		{"completed with a server-side web search", terminal("response.completed", "completed", "", webSearch+","+message), nil, usage.StopReasonEndTurn},
		{"completed with the call only in collected items", terminal("response.completed", "completed", "", ""), map[int64][]byte{0: []byte(functionCall)}, usage.StopReasonToolUse},
		{"websocket response.done", terminal("response.done", "completed", "", message), nil, usage.StopReasonEndTurn},
		{"incomplete at the output limit", terminal("response.incomplete", "incomplete", `,"incomplete_details":{"reason":"max_output_tokens"}`, message), nil, usage.StopReasonMaxTokens},
		{"incomplete by content filter", terminal("response.incomplete", "incomplete", `,"incomplete_details":{"reason":"content_filter"}`, ""), nil, usage.StopReasonRefusal},
		{"incomplete for another reason", terminal("response.incomplete", "incomplete", `,"incomplete_details":{"reason":"interrupted"}`, ""), nil, ""},
		{"failed", terminal("response.failed", "failed", "", ""), nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail, ok := ParseCodexUsageWithOutputItems([]byte(tc.payload), tc.collected, nil)
			if !ok || detail.StopReason != tc.want {
				t.Fatalf("stop reason = %q (ok %v), want %q", detail.StopReason, ok, tc.want)
			}
		})
	}
}
