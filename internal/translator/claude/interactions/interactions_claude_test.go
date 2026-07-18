package interactions

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToClaudeWithToolMessagesDirect(t *testing.T) {
	out := ConvertInteractionsRequestToClaude("claude-test", []byte(`{"model":"claude-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"toolu_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"toolu_1","result":{"ok":true}}]}`), false)
	if got := gjson.GetBytes(out, "system").String(); got != "be brief" {
		t.Fatalf("system = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "hi" {
		t.Fatalf("messages.0.content.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.1.content.0.type").String(); got != "tool_use" {
		t.Fatalf("messages.1.content.0.type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.2.content.0.tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("tool_use_id = %q, want toolu_1. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToClaudeStringInputDirect(t *testing.T) {
	out := ConvertInteractionsRequestToClaude("claude-test", []byte(`{"model":"claude-test","input":"hello"}`), false)
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "hello" {
		t.Fatalf("messages.0.content.0.text = %q, want hello. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToClaudeMapsGenerationConfigToolsAndStreamDirect(t *testing.T) {
	out := ConvertInteractionsRequestToClaude("claude-test", []byte(`{"model":"claude-test","stream":true,"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}],"tools":[{"type":"function","name":"lookup","description":"Lookup data","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],"generation_config":{"max_output_tokens":99,"top_p":0.7,"stop_sequences":["END"],"tool_choice":{"type":"function","name":"lookup"},"thinking_level":"high"}}`), false)
	if !gjson.GetBytes(out, "stream").Bool() {
		t.Fatalf("stream should be true when request body asks for stream. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 99 {
		t.Fatalf("max_tokens = %d, want 99. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.input_schema.properties.q.type").String(); got != "string" {
		t.Fatalf("tool schema type = %q, want string. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "lookup" {
		t.Fatalf("tool_choice.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got == "" {
		t.Fatalf("thinking config was not mapped. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToClaudeAcceptsImageContent(t *testing.T) {
	out := ConvertInteractionsRequestToClaude("claude-test", []byte(`{"model":"claude-test","input":[{"type":"user_input","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="}]}]}`), false)
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("content type = %q, want image. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.source.media_type").String(); got != "image/png" {
		t.Fatalf("media_type = %q, want image/png. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.source.data").String(); got != "aGVsbG8=" {
		t.Fatalf("data = %q, want aGVsbG8=. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToClaudePreservesNonImageMediaContent(t *testing.T) {
	out := ConvertInteractionsRequestToClaude("claude-test", []byte(`{"model":"claude-test","input":[{"type":"thought","content":[{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="},{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="},{"type":"document","mime_type":"application/pdf","data":"JVBERi0="}]}]}`), false)

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "text" {
		t.Fatalf("audio fallback type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.type").String(); got != "text" {
		t.Fatalf("video fallback type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.2.type").String(); got != "document" {
		t.Fatalf("document content type = %q, want document. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.#(type==\"image\")").Exists() {
		t.Fatalf("non-image media must not be converted to image. Output: %s", string(out))
	}
}

func TestConvertClaudeResponseToInteractionsNonStream(t *testing.T) {
	raw := []byte(`{"id":"msg_1","model":"claude-test","content":[{"type":"thinking","thinking":"reasoning"},{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}],"usage":{"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":1,"cache_creation_input_tokens":4,"thinking_tokens":5}}`)
	out := ConvertClaudeResponseToInteractionsNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "thought" {
		t.Fatalf("steps.0.type = %q, want thought. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.1.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.1.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.2.call_id").String(); got != "toolu_1" {
		t.Fatalf("steps.2.call_id = %q, want toolu_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_cached_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_cached_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertClaudeSSEToInteractionsNonStream(t *testing.T) {
	raw := []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":3,"output_tokens":0}}}
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}
data: {"type":"content_block_stop","index":0}
data: {"type":"message_delta","usage":{"output_tokens":2}}`)
	out := ConvertClaudeResponseToInteractionsNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertClaudeResponseToInteractionsStreamMergesUsageAndStatus(t *testing.T) {
	var param any
	var events [][]byte
	for _, raw := range [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":3,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","usage":{"output_tokens":2}}`),
	} {
		events = append(events, ConvertClaudeResponseToInteractions(context.Background(), "claude-test", nil, nil, raw, &param)...)
	}
	if payload := findClaudeInteractionsEventPayload(events, "interaction.status_update"); len(payload) == 0 {
		t.Fatalf("interaction.status_update event not found: %q", events)
	}
	payload := findClaudeInteractionsEventPayload(events, "interaction.completed")
	if got := gjson.GetBytes(payload, "interaction.usage.total_input_tokens").Int(); got != 3 {
		t.Fatalf("total_input_tokens = %d, want 3. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "interaction.usage.total_output_tokens").Int(); got != 2 {
		t.Fatalf("total_output_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "interaction.usage.total_tokens").Int(); got != 5 {
		t.Fatalf("total_tokens = %d, want 5. Payload: %s", got, string(payload))
	}
}

func TestConvertClaudeResponseToInteractionsStream(t *testing.T) {
	var param any
	events := ConvertClaudeResponseToInteractions(context.Background(), "claude-test", nil, nil, []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`), &param)
	payload := findClaudeInteractionsEventPayload(events, "step.delta")
	if len(payload) == 0 {
		t.Fatalf("step.delta event not found: %q", events)
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func findClaudeInteractionsEventPayload(events [][]byte, eventType string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if gjson.GetBytes(payload, "event_type").String() == eventType || gjson.GetBytes(payload, "type").String() == eventType {
				return payload
			}
		}
	}
	return nil
}
