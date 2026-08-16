package interactions

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToCodexWithToolMessagesDirect(t *testing.T) {
	out := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","content":[{"type":"text","text":"thinking"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`), false)
	if got := gjson.GetBytes(out, "instructions").String(); got != "be brief" {
		t.Fatalf("instructions = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("input.0.content.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "reasoning" {
		t.Fatalf("input.1.type = %q, want reasoning. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.type").String(); got != "function_call" {
		t.Fatalf("input.2.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.call_id").String(); got != "call_1" {
		t.Fatalf("function_call call_id = %q, want call_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.3.type").String(); got != "function_call_output" {
		t.Fatalf("input.3.type = %q, want function_call_output. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "contents").Exists() || gjson.GetBytes(out, "systemInstruction").Exists() {
		t.Fatalf("Codex request must not use foreign request shape. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToCodexPreservesNonImageMediaContent(t *testing.T) {
	out := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","input":[{"type":"model_output","content":[{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="},{"type":"video","mime_type":"video/mp4","data":"AAAAIGZ0eXA="},{"type":"document","mime_type":"application/pdf","data":"JVBERi0="}]}]}`), false)

	if got := gjson.GetBytes(out, "input.0.role").String(); got != "assistant" {
		t.Fatalf("input.0.role = %q, want assistant. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "input_audio" {
		t.Fatalf("audio content type = %q, want input_audio. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.1.content.0.type").String(); got != "input_file" {
		t.Fatalf("video content type = %q, want input_file. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.2.content.0.type").String(); got != "input_file" {
		t.Fatalf("document content type = %q, want input_file. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexPreservesTopLevelThinkingLevel(t *testing.T) {
	out := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","generation_config":{"thinking_level":"high"},"input":"hi"}`), true)
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexUsesBodyStream(t *testing.T) {
	out := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","stream":true,"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "stream").Bool(); !got {
		t.Fatalf("stream = %v, want true. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToCodexFunctionDeclarations(t *testing.T) {
	out := ConvertInteractionsRequestToCodex("codex-test", []byte(`{"model":"codex-test","input":"hi","tools":[{"function_declarations":[{"name":"lookup","description":"Lookup data","parameters":{"type":"object","$schema":"http://json-schema.org/draft-07/schema#","properties":{"q":{"type":"string"}}}}]}]}`), false)
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "tools.0.parameters.$schema").Exists() {
		t.Fatalf("tool parameters should not keep $schema. Output: %s", string(out))
	}
}

func TestConvertCodexResponseToInteractionsIncompleteTerminal(t *testing.T) {
	raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	nonStreamOut := ConvertCodexResponseToInteractionsNonStream(context.Background(), "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(nonStreamOut, "status").String(); got != "incomplete" {
		t.Fatalf("non-stream status = %q, want incomplete. Output: %s", got, nonStreamOut)
	}

	var param any
	streamOut := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, append([]byte("data: "), raw...), &param)
	payload := findCodexInteractionsEventPayload(streamOut, "interaction.completed")
	if len(payload) == 0 {
		t.Fatalf("stream incomplete event did not terminate interaction: %q", streamOut)
	}
	if got := gjson.GetBytes(payload, "interaction.status").String(); got != "incomplete" {
		t.Fatalf("stream status = %q, want incomplete. Payload: %s", got, payload)
	}
}

func TestConvertCodexResponseToInteractionsNonStream(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"usage":{"input_tokens":3,"output_tokens":2},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]},{"type":"reasoning","content":"thinking"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}]}}`)
	out := ConvertCodexResponseToInteractionsNonStream(context.Background(), "codex-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.1.type").String(); got != "thought" {
		t.Fatalf("steps.1.type = %q, want thought. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.2.type").String(); got != "function_call" {
		t.Fatalf("steps.2.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertCodexResponseToInteractionsStream(t *testing.T) {
	var param any
	events := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, []byte(`data: {"type":"response.output_text.delta","delta":"ok"}`), &param)
	payload := findCodexInteractionsEventPayload(events, "step.delta")
	if len(payload) == 0 {
		t.Fatalf("step.delta event not found: %q", events)
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func TestConvertCodexResponseToInteractionsStreamFunctionCallStartHasCallID(t *testing.T) {
	var param any
	events := ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`), &param)
	payload := findCodexInteractionsEventPayload(events, "step.start")
	if got := gjson.GetBytes(payload, "step.call_id").String(); got != "call_1" {
		t.Fatalf("step.call_id = %q, want call_1. Payload: %s", got, string(payload))
	}
}

func TestConvertCodexResponseToInteractionsStreamCompletesAfterSteps(t *testing.T) {
	var param any
	var events [][]byte
	for _, chunk := range [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"codex-test"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"我将调用工具。"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}"},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
	} {
		events = append(events, ConvertCodexResponseToInteractions(context.Background(), "codex-test", nil, nil, chunk, &param)...)
	}

	got := strings.Join(codexInteractionsEventNames(events), ",")
	want := "interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed,done"
	if got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	completed := findCodexInteractionsEventPayload(events, "interaction.completed")
	if gotTokens := gjson.GetBytes(completed, "interaction.usage.total_tokens").Int(); gotTokens != 3 {
		t.Fatalf("total_tokens = %d, want 3. Payload: %s", gotTokens, string(completed))
	}
}

func findCodexInteractionsEventPayload(events [][]byte, eventType string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		eventName := codexInteractionsFrameEventName(event)
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if codexInteractionsEventName(eventName, payload) == eventType {
				return payload
			}
		}
	}
	return nil
}

func codexInteractionsEventNames(events [][]byte) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		eventName := codexInteractionsFrameEventName(event)
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(line[len("data:"):])
			if name := codexInteractionsEventName(eventName, payload); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func codexInteractionsEventName(eventName string, payload []byte) string {
	if eventType := gjson.GetBytes(payload, "event_type").String(); eventType != "" {
		return eventType
	}
	if eventType := gjson.GetBytes(payload, "type").String(); eventType != "" {
		return eventType
	}
	return eventName
}

func codexInteractionsFrameEventName(event []byte) string {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("event:")) {
			return strings.TrimSpace(string(line[len("event:"):]))
		}
	}
	return ""
}
