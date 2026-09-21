package responses

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
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
		[]byte(`event: interaction.created
data: {"interaction":{"id":"interaction_1","model":"source-model"},"event_type":"interaction.created"}

`),
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
	argumentsDonePayload := findResponsesEventPayload(out, "response.function_call_arguments.done")
	if got := gjson.GetBytes(argumentsDonePayload, "item_id").String(); got != "call_1" {
		t.Fatalf("function args done item_id = %q, want call_1. Payload: %s", got, string(argumentsDonePayload))
	}
	if got := gjson.GetBytes(argumentsDonePayload, "arguments").String(); got != `{"location":"北京"}` {
		t.Fatalf("function args done arguments = %q, want full arguments. Payload: %s", got, string(argumentsDonePayload))
	}
	createdPayload := findResponsesEventPayload(out, "response.created")
	if got := gjson.GetBytes(createdPayload, "response.model").String(); got != "gpt-test" {
		t.Fatalf("response.created models = %q, want gpt-test", got)
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

func TestConvertInteractionsResponseToOpenAIResponsesStreamFunctionCallStartArguments(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"id":"call_1","type":"function_call","name":"lookup","arguments":{"q":"x"}},"event_type":"step.start"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", nil, nil, raw, &param)...)
	}

	gotEvents := strings.Join(responsesEventNames(out), ",")
	wantEvents := "response.output_item.added,response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done"
	if gotEvents != wantEvents {
		t.Fatalf("events = %s, want %s", gotEvents, wantEvents)
	}
	if payload := findResponsesEventPayload(out, "response.function_call_arguments.delta"); gjson.GetBytes(payload, "delta").String() != `{"q":"x"}` {
		t.Fatalf("function args delta = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.function_call_arguments.done"); gjson.GetBytes(payload, "arguments").String() != `{"q":"x"}` {
		t.Fatalf("function args done = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.output_item.done"); gjson.GetBytes(payload, "item.arguments").String() != `{"q":"x"}` {
		t.Fatalf("output item done = %s", string(payload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamFunctionCallEmptyArguments(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"id":"call_1","type":"function_call","name":"lookup","arguments":{}},"event_type":"step.start"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
		[]byte(`event: interaction.completed
data: {"interaction":{"id":"interaction_1","status":"completed","model":"gpt-test"},"event_type":"interaction.completed"}

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", nil, nil, raw, &param)...)
	}

	gotEvents := strings.Join(responsesEventNames(out), ",")
	wantEvents := "response.output_item.added,response.function_call_arguments.done,response.output_item.done,response.completed"
	if gotEvents != wantEvents {
		t.Fatalf("events = %s, want %s", gotEvents, wantEvents)
	}
	if payload := findResponsesEventPayload(out, "response.function_call_arguments.done"); gjson.GetBytes(payload, "arguments").String() != "{}" {
		t.Fatalf("function args done = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.output_item.done"); gjson.GetBytes(payload, "item.arguments").String() != "{}" {
		t.Fatalf("output item done = %s", string(payload))
	}
	if payload := findResponsesEventPayload(out, "response.completed"); gjson.GetBytes(payload, "response.output.0.arguments").String() != "{}" {
		t.Fatalf("completed output = %s", string(payload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamFunctionCallEventsAreIdempotent(t *testing.T) {
	var param any
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"id":"call_1","type":"function_call","name":"lookup","arguments":{"q":"x"}},"event_type":"step.start"}

`),
		[]byte(`event: step.start
data: {"index":0,"step":{"id":"call_1","type":"function_call","name":"lookup","arguments":{"q":"x"}},"event_type":"step.start"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
		[]byte(`event: step.stop
data: {"index":0,"event_type":"step.stop"}

`),
	} {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "gpt-test", nil, nil, raw, &param)...)
	}

	gotEvents := strings.Join(responsesEventNames(out), ",")
	wantEvents := "response.output_item.added,response.function_call_arguments.delta,response.function_call_arguments.done,response.output_item.done"
	if gotEvents != wantEvents {
		t.Fatalf("events = %s, want %s", gotEvents, wantEvents)
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

func testGPTResponsesReasoningSignature() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	payload[8] = 1
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(payload)
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamPreservesThoughtSignature(t *testing.T) {
	var param any
	signature := testGPTResponsesReasoningSignature()
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

func TestConvertInteractionsResponseToOpenAIResponsesStreamDropsForeignThoughtSignature(t *testing.T) {
	var param any
	foreignSignature := "foreign-gemini-signature"
	var out [][]byte
	for _, raw := range [][]byte{
		[]byte(`event: step.start
data: {"index":0,"step":{"type":"thought"},"event_type":"step.start"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"content":{"text":"thinking","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}

`),
		[]byte(`event: step.delta
data: {"index":0,"delta":{"signature":"` + foreignSignature + `","type":"thought_signature"},"event_type":"step.delta"}

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

	donePayload := findResponsesEventPayload(out, "response.output_item.done")
	if got := gjson.GetBytes(donePayload, "item.encrypted_content").String(); got != "" {
		t.Fatalf("done encrypted_content = %q, want empty for foreign signature. Payload: %s", got, string(donePayload))
	}
	if got := gjson.GetBytes(donePayload, "item.summary.0.text").String(); got != "thinking" {
		t.Fatalf("done summary = %q, want thinking. Payload: %s", got, string(donePayload))
	}
	completedPayload := findResponsesEventPayload(out, "response.completed")
	if got := gjson.GetBytes(completedPayload, "response.output.0.encrypted_content").String(); got != "" {
		t.Fatalf("completed encrypted_content = %q, want empty for foreign signature. Payload: %s", got, string(completedPayload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesNonStreamThoughtSignature(t *testing.T) {
	validSig := testGPTResponsesReasoningSignature()
	rawValid := []byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"thought","signature":"` + validSig + `","content":[{"type":"text","text":"thinking"}]}],"usage":{"total_tokens":1}}`)
	outValid := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, rawValid, nil)
	if got := gjson.GetBytes(outValid, "output.0.encrypted_content").String(); got != validSig {
		t.Fatalf("valid encrypted_content = %q, want %q. Output: %s", got, validSig, string(outValid))
	}
	if got := gjson.GetBytes(outValid, "output.0.summary.0.text").String(); got != "thinking" {
		t.Fatalf("summary = %q, want thinking. Output: %s", got, string(outValid))
	}

	rawForeign := []byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"thought","thought_signature":"foreign-gemini-signature","content":[{"type":"text","text":"thinking"}]}],"usage":{"total_tokens":1}}`)
	outForeign := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "gpt-test", []byte(`{"model":"gpt-test"}`), nil, rawForeign, nil)
	if got := gjson.GetBytes(outForeign, "output.0.encrypted_content").String(); got != "" {
		t.Fatalf("foreign encrypted_content = %q, want empty. Output: %s", got, string(outForeign))
	}
	if got := gjson.GetBytes(outForeign, "output.0.summary.0.text").String(); got != "thinking" {
		t.Fatalf("summary = %q, want thinking. Output: %s", got, string(outForeign))
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

func TestConvertOpenAIResponsesResponseToInteractionsIncompleteTerminal(t *testing.T) {
	t.Run("NonStream", func(t *testing.T) {
		raw := []byte(`{"id":"resp_1","status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		out := ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
		if got := gjson.GetBytes(out, "status").String(); got != "incomplete" {
			t.Fatalf("status = %q, want incomplete. Output: %s", got, string(out))
		}
		if gotText := gjson.GetBytes(out, "steps.0.content.0.text").String(); gotText != "partial" {
			t.Fatalf("step text = %q, want partial. Output: %s", gotText, string(out))
		}
		if gotTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTokens != 3 {
			t.Fatalf("total_tokens = %d, want 3. Output: %s", gotTokens, string(out))
		}
	})

	t.Run("Stream", func(t *testing.T) {
		var param any
		raw := []byte(`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","output":[{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
		out := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, raw, &param)
		if got := countInteractionsEventType(out, "interaction.completed"); got != 1 {
			t.Fatalf("interaction.completed count = %d, want 1", got)
		}
		if got := countInteractionsEventType(out, "done"); got != 1 {
			t.Fatalf("done count = %d, want 1", got)
		}
		deltaPayload := findInteractionsStepDeltaPayload(out)
		if gotText := gjson.GetBytes(deltaPayload, "delta.text").String(); gotText != "partial" {
			t.Fatalf("delta.text = %q, want partial. Payload: %s", gotText, string(deltaPayload))
		}
		completedPayload := findInteractionsEventPayload(out, "interaction.completed")
		if got := gjson.GetBytes(completedPayload, "interaction.status").String(); got != "incomplete" {
			t.Fatalf("interaction.status = %q, want incomplete. Payload: %s", got, string(completedPayload))
		}
		if gotTokens := gjson.GetBytes(completedPayload, "interaction.usage.total_tokens").Int(); gotTokens != 3 {
			t.Fatalf("total_tokens = %d, want 3. Payload: %s", gotTokens, string(completedPayload))
		}

		doneOut := ConvertOpenAIResponsesResponseToInteractions(context.Background(), "gpt-test", nil, nil, []byte(`data: [DONE]`), &param)
		if got := countInteractionsEventType(doneOut, "interaction.completed"); got != 0 {
			t.Fatalf("subsequent done interaction.completed count = %d, want 0", got)
		}
		if got := countInteractionsEventType(doneOut, "done"); got != 0 {
			t.Fatalf("subsequent done event count = %d, want 0", got)
		}
	})

	t.Run("CompletedControl", func(t *testing.T) {
		raw := []byte(`{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
		out := ConvertOpenAIResponsesResponseToInteractionsNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
		if got := gjson.GetBytes(out, "status").String(); got != "completed" {
			t.Fatalf("status = %q, want completed. Output: %s", got, string(out))
		}
	})
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

func TestConvertInteractionsResponseToOpenAIResponsesNonStream_PreservesEnvironmentID(t *testing.T) {
	raw := []byte(`{"id":"interaction_1","object":"interaction","environment_id":"env_abc123","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "antigravity-preview-05-2026", []byte(`{"model":"antigravity-preview-05-2026"}`), nil, raw, nil)
	if got := gjson.GetBytes(out, "environment_id").String(); got != "env_abc123" {
		t.Fatalf("environment_id = %q, want env_abc123. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStream_PreservesEnvironmentID(t *testing.T) {
	var param any
	var out [][]byte
	rawEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"interaction_1\",\"environment_id\":\"env_stream123\",\"model\":\"antigravity-preview-05-2026\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"interaction_1\",\"environment_id\":\"env_stream123\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	for _, raw := range rawEvents {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "antigravity-preview-05-2026", []byte(`{"model":"antigravity-preview-05-2026"}`), nil, raw, &param)...)
	}

	createdPayload := findResponsesEventPayload(out, "response.created")
	if got := gjson.GetBytes(createdPayload, "response.environment_id").String(); got != "env_stream123" {
		t.Fatalf("response.created environment_id = %q, want env_stream123. Payload: %s", got, string(createdPayload))
	}
	completedPayload := findResponsesEventPayload(out, "response.completed")
	if got := gjson.GetBytes(completedPayload, "response.environment_id").String(); got != "env_stream123" {
		t.Fatalf("response.completed environment_id = %q, want env_stream123. Payload: %s", got, string(completedPayload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesNonStreamRestoresAntigravityToolName(t *testing.T) {
	raw := []byte(`{
		"id":"interaction_1",
		"model":"antigravity-preview-05-2026",
		"steps":[
			{"type":"function_call","id":"call_1","name":"external_read_file","arguments":{"path":"/etc/hosts"}}
		]
	}`)
	out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "antigravity-preview-05-2026", []byte(`{"model":"antigravity-preview-05-2026"}`), nil, raw, nil)
	if got := gjson.GetBytes(out, "output.0.name").String(); got != "read_file" {
		t.Fatalf("output.0.name = %q, want read_file. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesStreamRestoresAntigravityToolName(t *testing.T) {
	var param any
	var out [][]byte
	rawEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"interaction_1\",\"model\":\"antigravity-preview-05-2026\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call_1\",\"name\":\"external_read_file\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"path\\\":\\\"/etc/hosts\\\"}\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"interaction_1\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	for _, raw := range rawEvents {
		out = append(out, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "antigravity-preview-05-2026", []byte(`{"model":"antigravity-preview-05-2026"}`), nil, raw, &param)...)
	}

	addedPayload := findResponsesEventPayload(out, "response.output_item.added")
	if got := gjson.GetBytes(addedPayload, "item.name").String(); got != "read_file" {
		t.Fatalf("stream item.name = %q, want read_file. Payload: %s", got, string(addedPayload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponsesPreservesNonCollidingAndNonAntigravityNames(t *testing.T) {
	// 1. Antigravity model with non-colliding external_ name: external_lookup must NOT be stripped.
	rawNonColliding := []byte(`{
		"id":"interaction_1",
		"model":"antigravity-preview-05-2026",
		"steps":[
			{"type":"function_call","id":"call_1","name":"external_lookup","arguments":{"q":"test"}}
		]
	}`)
	outNonColliding := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "antigravity-preview-05-2026", []byte(`{"model":"antigravity-preview-05-2026"}`), nil, rawNonColliding, nil)
	if got := gjson.GetBytes(outNonColliding, "output.0.name").String(); got != "external_lookup" {
		t.Fatalf("output.0.name = %q, want external_lookup (preserved). Output: %s", got, string(outNonColliding))
	}

	// 2. Non-antigravity model with external_read_file: must NOT be stripped.
	rawNonAnti := []byte(`{
		"id":"interaction_2",
		"model":"gemini-3.1-flash-lite",
		"steps":[
			{"type":"function_call","id":"call_2","name":"external_read_file","arguments":{"path":"/etc/hosts"}}
		]
	}`)
	outNonAnti := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.1-flash-lite", []byte(`{"model":"gemini-3.1-flash-lite"}`), nil, rawNonAnti, nil)
	if got := gjson.GetBytes(outNonAnti, "output.0.name").String(); got != "external_read_file" {
		t.Fatalf("output.0.name = %q, want external_read_file (preserved for non-antigravity). Output: %s", got, string(outNonAnti))
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_PreservesHTMLCharactersInToolCallArguments(t *testing.T) {
	command := `gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100`

	// 1. Non-stream test: function_call with 2>&1
	rawNonStream := []byte(fmt.Sprintf(`{
		"id":"interaction_test",
		"model":"devin/swe-2",
		"steps":[
			{"type":"function_call","id":"bash_1","name":"bash","arguments":{"command":%q,"timeout":60}}
		]
	}`, command))
	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", nil, nil, rawNonStream, nil)
	outNonStreamStr := string(outNonStream)
	if strings.Contains(outNonStreamStr, `\u003e`) || strings.Contains(outNonStreamStr, `\u0026`) {
		t.Fatalf("non-stream output contains escaped HTML characters: %s", outNonStreamStr)
	}
	if !strings.Contains(outNonStreamStr, "2>&1") {
		t.Fatalf("non-stream output should contain '2>&1': %s", outNonStreamStr)
	}

	// 2. Stream test: function_call delta and done with 2>&1
	var param any
	var outStream [][]byte
	rawEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"interaction_test\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":1,\"step\":{\"type\":\"function_call\",\"id\":\"bash_1\",\"name\":\"bash\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte(fmt.Sprintf("event: step.delta\ndata: {\"index\":1,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"command\\\":\\\"%s\\\",\\\"timeout\\\":60}\"},\"event_type\":\"step.delta\"}\n\n", command)),
		[]byte("event: step.stop\ndata: {\"index\":1,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"interaction_test\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	for _, raw := range rawEvents {
		outStream = append(outStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &param)...)
	}

	for _, frame := range outStream {
		frameStr := string(frame)
		if strings.Contains(frameStr, `\u003e`) || strings.Contains(frameStr, `\u0026`) {
			t.Fatalf("stream frame contains escaped HTML characters: %s", frameStr)
		}
	}

	donePayload := findResponsesEventPayload(outStream, "response.function_call_arguments.done")
	if donePayload == nil {
		t.Fatalf("missing response.function_call_arguments.done event")
	}
	if !strings.Contains(string(donePayload), "2>&1") {
		t.Fatalf("function_call_arguments.done should contain '2>&1': %s", string(donePayload))
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_LogReplayTwoToolCalls(t *testing.T) {
	// Replay the exact scenario from the log with separated tool calls:
	// Step 0: thought
	// Step 1: title_0 (title: Triage issue 5802)
	// Step 2: bash_1 (gh issue view ... 2>&1 | head -100)
	command := `gh issue view 5802 --json number,title,body,url,state,labels,assignees 2>&1 | head -100`

	var param any
	var outStream [][]byte
	rawEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"interaction_69c3126a-ab3\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"thought\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"thought_summary\",\"text\":\"I need to triage GitHub issue 5802.\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		// Tool call 1: title
		[]byte("event: step.start\ndata: {\"index\":1,\"step\":{\"type\":\"function_call\",\"id\":\"title_0\",\"call_id\":\"title_0\",\"name\":\"title\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":1,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"title\\\": \\\"Triage issue 5802\\\"}\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":1,\"event_type\":\"step.stop\"}\n\n"),
		// Tool call 2: bash
		[]byte("event: step.start\ndata: {\"index\":2,\"step\":{\"type\":\"function_call\",\"id\":\"bash_1\",\"call_id\":\"bash_1\",\"name\":\"bash\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte(fmt.Sprintf("event: step.delta\ndata: {\"index\":2,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"command\\\": \\\"%s\\\", \\\"timeout\\\": 60}\"},\"event_type\":\"step.delta\"}\n\n", command)),
		[]byte("event: step.stop\ndata: {\"index\":2,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"interaction_69c3126a-ab3\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}

	for _, raw := range rawEvents {
		outStream = append(outStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &param)...)
	}

	for _, frame := range outStream {
		frameStr := string(frame)
		if strings.Contains(frameStr, `\u003e`) || strings.Contains(frameStr, `\u0026`) {
			t.Fatalf("stream frame contains escaped HTML characters: %s", frameStr)
		}
	}

	completedPayload := findResponsesEventPayload(outStream, "response.completed")
	if completedPayload == nil {
		t.Fatalf("missing response.completed event")
	}

	outputItems := gjson.GetBytes(completedPayload, "response.output").Array()
	if len(outputItems) != 3 {
		t.Fatalf("expected 3 output items (thought, title, bash), got %d: %s", len(outputItems), string(completedPayload))
	}

	titleItem := outputItems[1]
	if titleItem.Get("name").String() != "title" || titleItem.Get("call_id").String() != "title_0" {
		t.Errorf("title item mismatch: %s", titleItem.Raw)
	}
	if titleItem.Get("arguments").String() != `{"title": "Triage issue 5802"}` {
		t.Errorf("title arguments = %s", titleItem.Get("arguments").String())
	}

	bashItem := outputItems[2]
	if bashItem.Get("name").String() != "bash" || bashItem.Get("call_id").String() != "bash_1" {
		t.Errorf("bash item mismatch: %s", bashItem.Raw)
	}
	if !strings.Contains(bashItem.Get("arguments").String(), "2>&1") {
		t.Errorf("bash arguments should contain '2>&1': %s", bashItem.Get("arguments").String())
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_FunctionCallHasStatus(t *testing.T) {
	// 1. Stream test: function_call added/done and response.completed items must carry status
	var param any
	rawEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"i1\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call_1\",\"call_id\":\"call_1\",\"name\":\"write_file\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/test\\\"}\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"i1\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}

	var outStream [][]byte
	for _, raw := range rawEvents {
		outStream = append(outStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &param)...)
	}

	addedPayload := findResponsesEventPayload(outStream, "response.output_item.added")
	if addedPayload == nil {
		t.Fatalf("missing response.output_item.added")
	}
	if got := gjson.GetBytes(addedPayload, "item.status").String(); got != "in_progress" {
		t.Fatalf("added item.status = %q, want in_progress. Payload: %s", got, string(addedPayload))
	}

	donePayload := findResponsesEventPayload(outStream, "response.output_item.done")
	if donePayload == nil {
		t.Fatalf("missing response.output_item.done")
	}
	if got := gjson.GetBytes(donePayload, "item.status").String(); got != "completed" {
		t.Fatalf("done item.status = %q, want completed. Payload: %s", got, string(donePayload))
	}

	completedPayload := findResponsesEventPayload(outStream, "response.completed")
	if completedPayload == nil {
		t.Fatalf("missing response.completed")
	}
	if got := gjson.GetBytes(completedPayload, "response.output.0.status").String(); got != "completed" {
		t.Fatalf("completed response.output.0.status = %q, want completed. Payload: %s", got, string(completedPayload))
	}

	// 2. Non-stream test: function_call output item must carry status: completed
	rawNonStream := []byte(`{
		"id":"i1",
		"model":"devin/swe-2",
		"steps":[
			{"type":"function_call","id":"call_1","name":"write_file","arguments":{"path":"/tmp/test"}}
		]
	}`)
	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", nil, nil, rawNonStream, nil)
	if got := gjson.GetBytes(outNonStream, "output.0.status").String(); got != "completed" {
		t.Fatalf("non-stream output.0.status = %q, want completed. Output: %s", got, string(outNonStream))
	}

	// 3. Truncation test: stream with incomplete/length produces response.incomplete
	var paramTrunc any
	rawTruncEvents := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"i2\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"call_2\",\"name\":\"write_file\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/t\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"i2\",\"status\":\"incomplete\",\"finish_reason\":\"length\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	var outTruncStream [][]byte
	for _, raw := range rawTruncEvents {
		outTruncStream = append(outTruncStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &paramTrunc)...)
	}

	incompletePayload := findResponsesEventPayload(outTruncStream, "response.incomplete")
	if incompletePayload == nil {
		t.Fatalf("expected response.incomplete event for truncated stream")
	}
	if got := gjson.GetBytes(incompletePayload, "response.status").String(); got != "incomplete" {
		t.Fatalf("response.status = %q, want incomplete", got)
	}
	if got := gjson.GetBytes(incompletePayload, "response.incomplete_details.reason").String(); got != "max_output_tokens" {
		t.Fatalf("response.incomplete_details.reason = %q, want max_output_tokens", got)
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_ContentFilterIncomplete(t *testing.T) {
	// 1. Stream
	var param any
	rawStream := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"i_cf\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"model_output\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"blocked\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"i_cf\",\"status\":\"incomplete\",\"finish_reason\":\"content_filter\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	var outStream [][]byte
	for _, raw := range rawStream {
		outStream = append(outStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &param)...)
	}
	incompletePayload := findResponsesEventPayload(outStream, "response.incomplete")
	if incompletePayload == nil {
		t.Fatalf("expected response.incomplete for content_filter")
	}
	if got := gjson.GetBytes(incompletePayload, "response.incomplete_details.reason").String(); got != "content_filter" {
		t.Fatalf("incomplete_details.reason = %q, want content_filter. Payload: %s", got, string(incompletePayload))
	}

	// 2. Non-stream
	rawNonStream := []byte(`{
		"id":"i_cf",
		"model":"devin/swe-2",
		"status":"incomplete",
		"finish_reason":"content_filter",
		"steps":[{"type":"model_output","content":[{"type":"text","text":"blocked"}]}]
	}`)
	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", nil, nil, rawNonStream, nil)
	if got := gjson.GetBytes(outNonStream, "status").String(); got != "incomplete" {
		t.Fatalf("non-stream status = %q, want incomplete", got)
	}
	if got := gjson.GetBytes(outNonStream, "incomplete_details.reason").String(); got != "content_filter" {
		t.Fatalf("non-stream incomplete_details.reason = %q, want content_filter", got)
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_MissingUsageDefaultsToZeros(t *testing.T) {
	// 1. Stream with no usage in interaction.completed
	var param any
	rawStream := [][]byte{
		[]byte("event: interaction.created\ndata: {\"interaction\":{\"id\":\"i_nousage\",\"model\":\"devin/swe-2\"},\"event_type\":\"interaction.created\"}\n\n"),
		[]byte("event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"model_output\"},\"event_type\":\"step.start\"}\n\n"),
		[]byte("event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"hello\"},\"event_type\":\"step.delta\"}\n\n"),
		[]byte("event: step.stop\ndata: {\"index\":0,\"event_type\":\"step.stop\"}\n\n"),
		[]byte("event: interaction.completed\ndata: {\"interaction\":{\"id\":\"i_nousage\",\"status\":\"completed\"},\"event_type\":\"interaction.completed\"}\n\n"),
		[]byte("event: done\ndata: [DONE]\n\n"),
	}
	var outStream [][]byte
	for _, raw := range rawStream {
		outStream = append(outStream, ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", nil, nil, raw, &param)...)
	}
	completedPayload := findResponsesEventPayload(outStream, "response.completed")
	if completedPayload == nil {
		t.Fatalf("missing response.completed")
	}
	if !gjson.GetBytes(completedPayload, "response.usage.input_tokens").Exists() {
		t.Fatalf("expected usage.input_tokens to exist. Payload: %s", string(completedPayload))
	}
	if got := gjson.GetBytes(completedPayload, "response.usage.input_tokens").Int(); got != 0 {
		t.Fatalf("usage.input_tokens = %d, want 0", got)
	}
	if !gjson.GetBytes(completedPayload, "response.usage.output_tokens").Exists() {
		t.Fatalf("expected usage.output_tokens to exist")
	}
	if !gjson.GetBytes(completedPayload, "response.usage.total_tokens").Exists() {
		t.Fatalf("expected usage.total_tokens to exist")
	}

	// 2. Non-stream with no usage
	rawNonStream := []byte(`{
		"id":"i_nousage",
		"model":"devin/swe-2",
		"status":"completed",
		"steps":[{"type":"model_output","content":[{"type":"text","text":"hello"}]}]
	}`)
	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", nil, nil, rawNonStream, nil)
	if !gjson.GetBytes(outNonStream, "usage.input_tokens").Exists() {
		t.Fatalf("expected non-stream usage.input_tokens to exist. Output: %s", string(outNonStream))
	}
	if !gjson.GetBytes(outNonStream, "usage.output_tokens").Exists() {
		t.Fatalf("expected non-stream usage.output_tokens to exist")
	}
	if !gjson.GetBytes(outNonStream, "usage.total_tokens").Exists() {
		t.Fatalf("expected non-stream usage.total_tokens to exist")
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_RestoresNamespaceAndCustomTool(t *testing.T) {
	origRequest := []byte(`{
		"model": "devin/gemini-3-7-flash",
		"tools": [
			{
				"type": "namespace",
				"name": "multi_agent_v1",
				"tools": [
					{"type": "function", "name": "close_agent", "description": "Close an agent"}
				]
			},
			{
				"type": "namespace",
				"name": "functions",
				"tools": [
					{"type": "custom", "name": "exec", "description": "Run custom command"}
				]
			}
		]
	}`)

	// Test NonStream
	rawNonStream := []byte(`{
		"id": "resp_1",
		"steps": [
			{
				"type": "function_call",
				"id": "call_1",
				"name": "multi_agent_v1__close_agent",
				"arguments": {"target": "agent_1"}
			},
			{
				"type": "function_call",
				"id": "call_2",
				"name": "functions__exec",
				"arguments": {"input": "echo hi"}
			}
		]
	}`)

	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, rawNonStream, nil)

	// Item 0: multi_agent_v1__close_agent -> name: "close_agent", namespace: "multi_agent_v1", type: "function_call"
	if got := gjson.GetBytes(outNonStream, "output.0.name").String(); got != "close_agent" {
		t.Errorf("output.0.name = %q, want close_agent", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.0.namespace").String(); got != "multi_agent_v1" {
		t.Errorf("output.0.namespace = %q, want multi_agent_v1", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.0.type").String(); got != "function_call" {
		t.Errorf("output.0.type = %q, want function_call", got)
	}

	// Item 1: functions__exec -> name: "exec", namespace: "functions", type: "custom_tool_call"
	if got := gjson.GetBytes(outNonStream, "output.1.name").String(); got != "exec" {
		t.Errorf("output.1.name = %q, want exec", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.1.namespace").String(); got != "functions" {
		t.Errorf("output.1.namespace = %q, want functions", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.1.type").String(); got != "custom_tool_call" {
		t.Errorf("output.1.type = %q, want custom_tool_call", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.1.input").String(); got != "echo hi" {
		t.Errorf("output.1.input = %q, want echo hi", got)
	}
	if gjson.GetBytes(outNonStream, "output.1.arguments").Exists() {
		t.Errorf("output.1.arguments should not exist for custom_tool_call")
	}

	// Test Stream
	var param any
	streamChunk := []byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_1","name":"multi_agent_v1__close_agent","arguments":"{\"target\":\"agent_1\"}"}}`)
	events := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, streamChunk, &param)
	foundAdded := false
	for _, ev := range events {
		evStr := string(ev)
		if strings.Contains(evStr, "response.output_item.added") {
			foundAdded = true
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "item.name").String(); got != "close_agent" {
				t.Errorf("stream item.name = %q, want close_agent", got)
			}
			if got := gjson.Get(data, "item.namespace").String(); got != "multi_agent_v1" {
				t.Errorf("stream item.namespace = %q, want multi_agent_v1", got)
			}
			if got := gjson.Get(data, "item.type").String(); got != "function_call" {
				t.Errorf("stream item.type = %q, want function_call", got)
			}
		}
	}
	if !foundAdded {
		t.Fatalf("expected response.output_item.added event in stream")
	}

	// Test Stream done
	doneChunk := []byte(`{"event_type":"step.stop","index":0}`)
	eventsDone := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, doneChunk, &param)
	foundDone := false
	for _, ev := range eventsDone {
		evStr := string(ev)
		if strings.Contains(evStr, "response.output_item.done") {
			foundDone = true
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "item.name").String(); got != "close_agent" {
				t.Errorf("stream done item.name = %q, want close_agent", got)
			}
			if got := gjson.Get(data, "item.namespace").String(); got != "multi_agent_v1" {
				t.Errorf("stream done item.namespace = %q, want multi_agent_v1", got)
			}
		}
	}
	if !foundDone {
		t.Fatalf("expected response.output_item.done event in stream")
	}

	// Test Stream Custom Tool (start -> delta -> stop sequence)
	var paramCustom any
	streamCustomStart := []byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_2","name":"functions__exec"}}`)
	eventsStart := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, streamCustomStart, &paramCustom)
	foundCustomAdded := false
	for _, ev := range eventsStart {
		evStr := string(ev)
		if strings.Contains(evStr, "response.output_item.added") {
			foundCustomAdded = true
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "item.type").String(); got != "custom_tool_call" {
				t.Errorf("custom stream item.type = %q, want custom_tool_call", got)
			}
			if got := gjson.Get(data, "item.name").String(); got != "exec" {
				t.Errorf("custom stream item.name = %q, want exec", got)
			}
			if got := gjson.Get(data, "item.namespace").String(); got != "functions" {
				t.Errorf("custom stream item.namespace = %q, want functions", got)
			}
		}
		if strings.Contains(evStr, "response.custom_tool_call_input.done") {
			t.Fatalf("custom_tool_call_input.done should not be emitted on step.start")
		}
	}
	if !foundCustomAdded {
		t.Fatalf("expected response.output_item.added for custom tool")
	}

	streamCustomDelta := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"input\":\"pwd\"}"}}`)
	eventsDelta := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, streamCustomDelta, &paramCustom)
	for _, ev := range eventsDelta {
		if strings.Contains(string(ev), "response.function_call_arguments") {
			t.Fatalf("function_call_arguments events should not be emitted for custom tool")
		}
	}

	streamCustomStop := []byte(`{"event_type":"step.stop","index":0}`)
	eventsStop := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/gemini-3-7-flash", origRequest, nil, streamCustomStop, &paramCustom)
	customInputDoneCount := 0
	customItemDoneCount := 0
	for _, ev := range eventsStop {
		evStr := string(ev)
		if strings.Contains(evStr, "response.custom_tool_call_input.done") {
			customInputDoneCount++
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "input").String(); got != "pwd" {
				t.Errorf("custom stream input = %q, want pwd", got)
			}
		}
		if strings.Contains(evStr, "response.output_item.done") {
			customItemDoneCount++
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "item.input").String(); got != "pwd" {
				t.Errorf("custom stream done item.input = %q, want pwd", got)
			}
			if got := gjson.Get(data, "item.type").String(); got != "custom_tool_call" {
				t.Errorf("custom stream done item.type = %q, want custom_tool_call", got)
			}
		}
	}
	if customInputDoneCount != 1 {
		t.Fatalf("expected exactly 1 response.custom_tool_call_input.done, got %d", customInputDoneCount)
	}
	if customItemDoneCount != 1 {
		t.Fatalf("expected exactly 1 response.output_item.done, got %d", customItemDoneCount)
	}
}

func TestConvertInteractionsResponseToOpenAIResponses_AntigravityCustomToolRestoresNameAndType(t *testing.T) {
	origRequest := []byte(`{
		"model": "antigravity-preview-05-2026",
		"tools": [
			{"type": "custom", "name": "read_file", "description": "Read a file"}
		]
	}`)
	rawNonStream := []byte(`{
		"id": "resp_anti_custom",
		"steps": [
			{
				"type": "function_call",
				"id": "call_1",
				"name": "external_read_file",
				"arguments": {"input": "/path/to/file"}
			}
		]
	}`)

	outNonStream := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "antigravity-preview-05-2026", origRequest, nil, rawNonStream, nil)
	if got := gjson.GetBytes(outNonStream, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("output.0.type = %q, want custom_tool_call", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.0.name").String(); got != "read_file" {
		t.Fatalf("output.0.name = %q, want read_file", got)
	}
	if got := gjson.GetBytes(outNonStream, "output.0.input").String(); got != "/path/to/file" {
		t.Fatalf("output.0.input = %q, want /path/to/file", got)
	}
	if gjson.GetBytes(outNonStream, "output.0.arguments").Exists() {
		t.Fatalf("output.0.arguments unexpectedly exists for custom_tool_call")
	}

	// Test Stream (start -> delta -> stop)
	var param any
	streamChunk := []byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","call_id":"call_1","name":"external_read_file"}}`)
	eventsStart := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "antigravity-preview-05-2026", origRequest, nil, streamChunk, &param)
	foundAdded := false
	for _, ev := range eventsStart {
		evStr := string(ev)
		if strings.Contains(evStr, "response.output_item.added") {
			foundAdded = true
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "item.type").String(); got != "custom_tool_call" {
				t.Fatalf("stream item.type = %q, want custom_tool_call", got)
			}
			if got := gjson.Get(data, "item.name").String(); got != "read_file" {
				t.Fatalf("stream item.name = %q, want read_file", got)
			}
		}
		if strings.Contains(evStr, "response.custom_tool_call_input.done") {
			t.Fatalf("custom_tool_call_input.done should not be emitted on step.start")
		}
	}
	if !foundAdded {
		t.Fatalf("expected response.output_item.added event")
	}

	streamDelta := []byte(`{"event_type":"step.delta","index":0,"delta":{"type":"arguments_delta","arguments":"{\"input\":\"/path/to/file\"}"}}`)
	_ = ConvertInteractionsResponseToOpenAIResponses(context.Background(), "antigravity-preview-05-2026", origRequest, nil, streamDelta, &param)

	streamStop := []byte(`{"event_type":"step.stop","index":0}`)
	eventsStop := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "antigravity-preview-05-2026", origRequest, nil, streamStop, &param)
	customDoneCount := 0
	for _, ev := range eventsStop {
		evStr := string(ev)
		if strings.Contains(evStr, "response.custom_tool_call_input.done") {
			customDoneCount++
			data := strings.TrimPrefix(evStr, "data: ")
			if got := gjson.Get(data, "input").String(); got != "/path/to/file" {
				t.Fatalf("stream custom_tool_call_input.done = %q, want /path/to/file", got)
			}
		}
	}
	if customDoneCount != 1 {
		t.Fatalf("expected exactly 1 response.custom_tool_call_input.done event on step.stop, got %d", customDoneCount)
	}
}
