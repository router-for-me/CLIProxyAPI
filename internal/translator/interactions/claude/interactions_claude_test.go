package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToInteractionsMapsMessagesToolsAndStream(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","stream":true,"max_tokens":1024,"tools":[{"name":"get_weather","description":"Weather","input_schema":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"messages":[{"role":"user","content":[{"type":"text","text":"今天北京的天气怎么样？"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, true)
	if got := gjson.GetBytes(out, "model").String(); got != "gemini-3.1-flash-lite" {
		t.Fatalf("model = %q, want gemini-3.1-flash-lite. Output: %s", got, string(out))
	}
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 1024 {
		t.Fatalf("max_output_tokens = %d, want 1024. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "今天北京的天气怎么样？" {
		t.Fatalf("input text = %q. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.location.type").String(); got != "string" {
		t.Fatalf("tool schema was not mapped. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
}

func TestConvertClaudeRequestToInteractionsMapsToolUseAndResult(t *testing.T) {
	raw := []byte(`{"model":"gemini-3.1-flash-lite","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"location":"北京"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"晴"}]}]}`)
	out := ConvertClaudeRequestToInteractions("gemini-3.1-flash-lite", raw, false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "toolu_1" {
		t.Fatalf("call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.result").String(); got != "晴" {
		t.Fatalf("result = %q, want 晴. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToClaudeStream(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`event: interaction.created
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`event: step.start
data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"type":"text","text":"北京今天晴"},"event_type":"step.delta"}`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite","usage":{"total_input_tokens":3,"total_output_tokens":4}},"event_type":"interaction.completed"}`),
		[]byte(`event: done
data: [DONE]`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "message_start"); gjson.GetBytes(payload, "message.model").String() != "gemini-3.1-flash-lite" {
		t.Fatalf("message_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.text").String() != "北京今天晴" {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "usage.output_tokens").Int() != 4 {
		t.Fatalf("message_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_stop"); gjson.GetBytes(payload, "type").String() != "message_stop" {
		t.Fatalf("message_stop payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamToolCall(t *testing.T) {
	var param any
	var out [][]byte
	chunks := [][]byte{
		[]byte(`data: {"interaction":{"id":"interaction_1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`),
		[]byte(`data: {"index":0,"step":{"type":"function_call","id":"toolu_1","signature":"sig_1","name":"get_weather","arguments":{}},"event_type":"step.start"}`),
		[]byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"},"event_type":"step.delta"}`),
		[]byte(`data: {"index":0,"event_type":"step.stop"}`),
		[]byte(`data: {"interaction":{"usage":{"total_input_tokens":1,"total_output_tokens":2}},"event_type":"interaction.completed"}`),
	}
	for _, chunk := range chunks {
		out = append(out, ConvertInteractionsResponseToClaude(context.Background(), "gemini-3.1-flash-lite", nil, nil, chunk, &param)...)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.type").String() != "tool_use" {
		t.Fatalf("content_block_start payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_start"); gjson.GetBytes(payload, "content_block.signature").String() != "sig_1" {
		t.Fatalf("content_block_start signature payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "content_block_delta"); gjson.GetBytes(payload, "delta.partial_json").String() != `{"location":"北京"}` {
		t.Fatalf("content_block_delta payload = %s", payload)
	}
	if payload := findClaudeEventPayload(out, "message_delta"); gjson.GetBytes(payload, "delta.stop_reason").String() != "tool_use" {
		t.Fatalf("message_delta payload = %s", payload)
	}
}

func TestConvertInteractionsResponseToClaudeStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToClaude(context.Background(), "claude-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_tokens":8}}}`), &param)
	payload := findClaudeEventPayload(out, "message_delta")
	if len(payload) == 0 {
		t.Fatalf("message_delta payload not found")
	}
	if got := gjson.GetBytes(payload, "usage.input_tokens").Int(); got != 2 {
		t.Fatalf("input_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "usage.output_tokens").Int(); got != 6 {
		t.Fatalf("output_tokens = %d, want 6. Payload: %s", got, string(payload))
	}
}

func TestConvertInteractionsResponseToClaudeNonStream(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","model":"gemini-3.1-flash-lite","steps":[{"type":"model_output","content":[{"type":"text","text":"ok"}]},{"type":"function_call","call_id":"toolu_1","signature":"sig_1","name":"lookup","arguments":{"q":"x"}}],"usage":{"total_input_tokens":3,"total_output_tokens":4}}`)
	out := ConvertInteractionsResponseToClaudeNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.type").String(); got != "tool_use" {
		t.Fatalf("tool block type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.1.signature").String(); got != "sig_1" {
		t.Fatalf("tool signature = %q, want sig_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "stop_reason").String(); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 3 {
		t.Fatalf("input_tokens = %d, want 3. Output: %s", got, string(out))
	}
}

func findClaudeEventPayload(events [][]byte, eventName string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		if !bytes.Contains(event, []byte("event: "+eventName)) {
			continue
		}
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, prefix) {
				return bytes.TrimSpace(line[len(prefix):])
			}
		}
	}
	return nil
}
