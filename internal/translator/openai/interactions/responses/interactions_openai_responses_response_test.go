package responses

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsResponseToOpenAIResponsesNonStream(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.content.0.text").String(); got != "ok" {
		t.Fatalf("response text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("usage.total_tokens = %d, want 3. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStream(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.delta
data: {"index":0,"delta":{"content":{"text":"thinking","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}

`),
		[]byte(`event: step.delta
data: {"index":1,"delta":{"text":"I will call a tool.","type":"text"},"event_type":"step.delta"}

`),
		[]byte(`event: step.start
data: {"index":2,"step":{"id":"call_1","type":"function_call","name":"get_weather","arguments":{}},"event_type":"step.start"}

`),
		[]byte(`event: step.delta
data: {"index":2,"delta":{"arguments":"{\"location\":\"北京\"}","type":"arguments_delta"},"event_type":"step.delta"}

`),
		[]byte(`event: step.stop
data: {"index":2,"event_type":"step.stop"}

`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","status":"completed","usage":{"total_tokens":399,"total_input_tokens":123,"total_cached_tokens":5,"total_output_tokens":36,"total_thought_tokens":240},"created":"2026-07-06T06:01:35Z","object":"interaction","model":"gpt-test"},"event_type":"interaction.completed"}

`),
		[]byte(`event: done
data: [DONE]

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, raw, &param)...)
	}

	if payload := findResponsesEventPayload(out, "response.output_text.delta"); gjson.GetBytes(payload, "delta").String() != "I will call a tool." {
		t.Fatalf("output_text delta payload = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.function_call_arguments.delta"); gjson.GetBytes(payload, "delta").String() != `{"location":"北京"}` {
		t.Fatalf("function args delta payload = %s", string(payload))
	}
	completedPayload := findResponsesEventPayload(out, "response.completed")
	if got := gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int(); got != 399 {
		t.Fatalf("total_tokens = %d, want 399. Payload: %s", got, string(completedPayload))
	}
	if got := gjson.GetBytes(completedPayload, "response.usage.output_tokens_details.reasoning_tokens").Int(); got != 240 {
		t.Fatalf("reasoning_tokens = %d, want 240. Payload: %s", got, string(completedPayload))
	}
	if got := strings.Join(responsesEventNames(out), ","); !strings.Contains(got, "response.completed") {
		t.Fatalf("events = %s, want response.completed", got)
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamModelOutputDoneIncludesText(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"id":"msg_1","type":"model_output"},"event_type":"step.start"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"text":"hello","type":"text"},"event_type":"step.delta"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"text":" world","type":"text"},"event_type":"step.delta"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, raw, &param)...)
	}

	if payload := findResponsesEventPayload(out, "response.output_text.done"); gjson.GetBytes(payload, "text").String() != "hello world" {
		t.Fatalf("output_text done payload = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.content_part.done"); gjson.GetBytes(payload, "part.text").String() != "hello world" {
		t.Fatalf("content_part done payload = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.output_item.done"); gjson.GetBytes(payload, "item.content.0.text").String() != "hello world" {
		t.Fatalf("output_item done payload = %s", string(payload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamPreservesThoughtSignature(t *testing.T) {
	var param any
	signature := "EtoRtestThoughtSignature"
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"type":"thought"},"event_type":"step.start"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"content":{"text":"thinking","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"signature":"` + signature + `","type":"thought_signature"},"event_type":"step.delta"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","status":"completed","object":"interaction","model":"gpt-test"},"event_type":"interaction.completed"}

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, raw, &param)...)
	}

	if got := strings.Join(responsesEventNames(out), ","); strings.Contains(got, "response.output_text.delta") {
		t.Fatalf("events = %s, did not expect output_text delta for thought signature", got)
	}
	donePayload := findResponsesEventPayload(out, "response.output_item.done")
	if got := gjson.GetBytes(donePayload, "item.encrypted_content").String(); got != signature {
		t.Fatalf("done encrypted_content = %q, want %q. Payload: %s", got, signature, string(donePayload))
	}
	if got := gjson.GetBytes(donePayload, "item.summary.0.text").String(); got != "thinking" {
		t.Fatalf("done summary = %q, want thinking. Payload: %s", got, string(donePayload))
	}
	completedPayload := findResponsesEventPayload(out, "response.completed")
	if got := gjson.GetBytes(completedPayload, "response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed encrypted_content = %q, want %q. Payload: %s", got, signature, string(completedPayload))
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsNonStreamFunctionCall(t *testing.T) {
	raw := []byte(`{"id":"resp_1","output":[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	out := ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "function_call" {
		t.Fatalf("step type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(out, "steps.0.name").String(); got != "lookup" {
		t.Fatalf("name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "steps.0.call_id").String(); got != "call_1" {
		t.Fatalf("call_id = %q, want call_1", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsNonStreamFunctionCallStringArgs(t *testing.T) {
	raw := []byte(`{"id":"resp_1","output":[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	out := ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "function_call" {
		t.Fatalf("step type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(out, "steps.0.arguments.q").String(); got != "x" {
		t.Fatalf("arguments.q = %q, want x", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsNonStreamUsageDetails(t *testing.T) {
	raw := []byte(`{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":13,"total_tokens":24,"input_tokens_details":{"cached_tokens":5},"output_tokens_details":{"reasoning_tokens":7}}}`)
	out := ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "id").String(); got != "resp_1" {
		t.Fatalf("id = %q, want resp_1. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 11 {
		t.Fatalf("usage.input_tokens = %d, want 11. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 13 {
		t.Fatalf("usage.output_tokens = %d, want 13. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.reasoning_tokens").Int(); got != 7 {
		t.Fatalf("usage.reasoning_tokens = %d, want 7. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.cached_tokens").Int(); got != 5 {
		t.Fatalf("usage.cached_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamFunctionCallCallID(t *testing.T) {
	var param any
	raw := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_stream_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`)
	out := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)
	payload := findInteractionsStepDeltaPayload(out)
	if len(payload) == 0 {
		t.Fatalf("step.delta payload not found")
	}
	startPayload := findInteractionsEventPayload(out, "step.start")
	if got := gjson.GetBytes(startPayload, "step.id").String(); got != "call_stream_1" {
		t.Fatalf("step.id = %q, want call_stream_1", got)
	}
	if got := gjson.GetBytes(payload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamSkipsDoneArgumentsAfterDelta(t *testing.T) {
	var param any
	deltaRaw := []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_1","call_id":"call_1","delta":"{\"q\":\"x\"}"}`)
	deltaOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, deltaRaw, &param)
	payload := findInteractionsStepDeltaPayload(deltaOut)
	if len(payload) == 0 {
		t.Fatalf("delta step.delta payload not found")
	}
	if got := gjson.GetBytes(payload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string. Payload: %s", got, string(payload))
	}

	doneRaw := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`)
	doneOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, doneRaw, &param)
	if got := countInteractionsEventType(doneOut, "step.delta"); got != 0 {
		t.Fatalf("done step.delta count = %d, want 0", got)
	}
	if got := countInteractionsEventType(doneOut, "step.stop"); got != 1 {
		t.Fatalf("done step.stop count = %d, want 1", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamSkipsDoneTextAfterDelta(t *testing.T) {
	var param any
	deltaRaw := []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`)
	deltaOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, deltaRaw, &param)
	payload := findInteractionsStepDeltaPayload(deltaOut)
	if len(payload) == 0 {
		t.Fatalf("delta step.delta payload not found")
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "hi" {
		t.Fatalf("delta.text = %q, want hi. Payload: %s", got, string(payload))
	}

	doneRaw := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"hi"}]}}`)
	doneOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, doneRaw, &param)
	if got := countInteractionsEventType(doneOut, "step.delta"); got != 0 {
		t.Fatalf("done step.delta count = %d, want 0", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamSkipsDoneTextAfterUnkeyedDelta(t *testing.T) {
	var param any
	deltaRaw := []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"hi"}`)
	deltaOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, deltaRaw, &param)
	payload := findInteractionsStepDeltaPayload(deltaOut)
	if len(payload) == 0 {
		t.Fatalf("delta step.delta payload not found")
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "hi" {
		t.Fatalf("delta.text = %q, want hi. Payload: %s", got, string(payload))
	}

	doneRaw := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"hi"}]}}`)
	doneOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, doneRaw, &param)
	if got := countInteractionsEventType(doneOut, "step.delta"); got != 0 {
		t.Fatalf("done step.delta count = %d, want 0", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamCompletedOutputFallback(t *testing.T) {
	var param any
	raw := []byte(`{"type":"response.completed","response":{"output":[{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"final"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	out := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)
	payload := findInteractionsStepDeltaPayload(out)
	if len(payload) == 0 {
		t.Fatalf("fallback step.delta payload not found")
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "final" {
		t.Fatalf("delta.text = %q, want final. Payload: %s", got, string(payload))
	}
	if got := countInteractionsEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1", got)
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamEmitsDone(t *testing.T) {
	var param any
	completedRaw := []byte(`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	completedOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, completedRaw, &param)
	doneOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, []byte(`data: [DONE]`), &param)

	if got := countInteractionsEventType(completedOut, "interaction.completed"); got != 1 {
		t.Fatalf("completed interaction.completed count = %d, want 1", got)
	}
	if got := countInteractionsEventType(completedOut, "done"); got != 1 {
		t.Fatalf("completed done count = %d, want 1", got)
	}
	if got := countInteractionsEventType(doneOut, "interaction.completed"); got != 0 {
		t.Fatalf("done interaction.completed count = %d, want 0", got)
	}
	if got := countInteractionsEventType(doneOut, "done"); got != 0 {
		t.Fatalf("done event count = %d, want 0", got)
	}
	if payload := findInteractionsEventPayload(completedOut, "done"); string(payload) != "[DONE]" {
		t.Fatalf("done payload = %q, want [DONE]", string(payload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`), &param)
	payload := findResponsesEventPayload(out, "response.completed")
	if len(payload) == 0 {
		t.Fatalf("response.completed payload not found")
	}
	if got := gjson.GetBytes(payload, "response.usage.input_tokens").Int(); got != 2 {
		t.Fatalf("input_tokens = %d, want 2. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "response.usage.output_tokens").Int(); got != 6 {
		t.Fatalf("output_tokens = %d, want 6. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "response.usage.output_tokens_details.reasoning_tokens").Int(); got != 3 {
		t.Fatalf("reasoning_tokens = %d, want 3. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens").Int(); got != 1 {
		t.Fatalf("cached_tokens = %d, want 1. Payload: %s", got, string(payload))
	}
	if got := gjson.GetBytes(payload, "response.usage.total_tokens").Int(); got != 11 {
		t.Fatalf("total_tokens = %d, want 11. Payload: %s", got, string(payload))
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamCreatedThenDelta(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-test"}}`),
		[]byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`),
	} {
		out = append(out, ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)...)
	}

	got := strings.Join(interactionsEventNames(out), ",")
	want := "interaction.created,interaction.status_update,step.start,step.delta"
	if got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	payload := findInteractionsEventPayload(out, "interaction.status_update")
	if gotID := gjson.GetBytes(payload, "interaction_id").String(); gotID != "resp_1" {
		t.Fatalf("interaction_id = %q, want resp_1. Payload: %s", gotID, string(payload))
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamCompletesAfterSteps(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"我将调用工具。"}`),
		[]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"weather\"}"}}`),
		[]byte(`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
	} {
		out = append(out, ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)...)
	}

	got := strings.Join(interactionsEventNames(out), ",")
	want := "interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed,done"
	if got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	completedPayload := findInteractionsEventPayload(out, "interaction.completed")
	if gotTokens := gjson.GetBytes(completedPayload, "interaction.usage.total_tokens").Int(); gotTokens != 3 {
		t.Fatalf("total_tokens = %d, want 3. Payload: %s", gotTokens, string(completedPayload))
	}
}

func TestConvertOpenAIResponsesResponseToInteractionsStreamSkipsCompletedTextAfterUnkeyedDelta(t *testing.T) {
	var param any
	deltaRaw := []byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"delta":"final"}`)
	deltaOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, deltaRaw, &param)
	payload := findInteractionsStepDeltaPayload(deltaOut)
	if len(payload) == 0 {
		t.Fatalf("delta step.delta payload not found")
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "final" {
		t.Fatalf("delta.text = %q, want final. Payload: %s", got, string(payload))
	}

	raw := []byte(`{"type":"response.completed","response":{"output":[{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"final"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	out := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)
	if got := countInteractionsEventType(out, "step.delta"); got != 0 {
		t.Fatalf("completed step.delta count = %d, want 0", got)
	}
	if got := countInteractionsEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1", got)
	}
}

func findInteractionsStepDeltaPayload(events [][]byte) []byte {
	return findInteractionsEventPayload(events, "step.delta")
}

func findInteractionsEventPayload(events [][]byte, eventType string) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if interactionsEventName(event, payload) == eventType {
			return payload
		}
	}
	return nil
}

func ssePayload(event []byte) []byte {
	const prefix = "\ndata: "
	idx := bytes.Index(event, []byte(prefix))
	if idx < 0 {
		return nil
	}
	return event[idx+len(prefix):]
}

func countInteractionsEventType(events [][]byte, eventType string) int {
	count := 0
	for _, event := range events {
		payload := ssePayload(event)
		if interactionsEventName(event, payload) == eventType {
			count++
		}
	}
	return count
}

func interactionsEventNames(events [][]byte) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		payload := ssePayload(event)
		if name := interactionsEventName(event, payload); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func interactionsEventName(event, payload []byte) string {
	if eventType := gjson.GetBytes(payload, "event_type").String(); eventType != "" {
		return eventType
	}
	const prefix = "event: "
	lineEnd := bytes.IndexByte(event, '\n')
	if lineEnd < 0 || !bytes.HasPrefix(event, []byte(prefix)) {
		return ""
	}
	return string(event[len(prefix):lineEnd])
}

func findResponsesEventPayload(events [][]byte, eventType string) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if gjson.GetBytes(payload, "type").String() == eventType {
			return payload
		}
	}
	return nil
}

func responsesEventNames(events [][]byte) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		payload := ssePayload(event)
		if name := gjson.GetBytes(payload, "type").String(); name != "" {
			names = append(names, name)
		}
	}
	return names
}
