package interactions

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToGeminiStringInput(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":"hello"}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("role = %q, want user", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hello" {
		t.Fatalf("text = %q, want hello", got)
	}
}

func TestConvertInteractionsRequestToGeminiSystemAndGenerationConfig(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","system_instruction":{"text":"be brief"},"generation_config":{"max_output_tokens":32,"top_p":0.8},"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("systemInstruction = %q, want be brief", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("topP = %v, want 0.8", got)
	}
}

func TestConvertInteractionsRequestToGeminiStringSystemInstruction(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","system_instruction":"be brief","input":"hi"}`), false)
	if got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("systemInstruction.parts.0.text = %q, want be brief. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToInteractionsStringSystemInstruction(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","systemInstruction":{"parts":[{"text":"be brief"},{"text":"answer directly"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	sys := gjson.GetBytes(out, "system_instruction")
	if sys.Type != gjson.String {
		t.Fatalf("system_instruction type = %v, want string. Output: %s", sys.Type, string(out))
	}
	if got := sys.String(); got != "be brief\nanswer directly" {
		t.Fatalf("system_instruction = %q, want merged text. Output: %s", got, string(out))
	}
	if gjson.GetBytes(out, "system_instruction.parts").Exists() {
		t.Fatalf("system_instruction.parts should not be forwarded. Output: %s", string(out))
	}
}

func TestConvertGeminiResponseToInteractionsNonStream(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`))
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "model_output" {
		t.Fatalf("step type = %q, want model_output", got)
	}
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("text = %q, want ok", got)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 3 {
		t.Fatalf("total tokens = %d, want 3", got)
	}
}

func TestConvertInteractionsResponseToGeminiStreamFunctionCall(t *testing.T) {
	var param any
	created := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"interaction":{"id":"i1","model":"gemini-3.1-flash-lite"},"event_type":"interaction.created"}`), &param)
	if len(created) != 0 {
		t.Fatalf("created output count = %d, want 0", len(created))
	}
	start := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"index":0,"step":{"type":"function_call","id":"call_1","signature":"sig_1","name":"get_weather","arguments":{}},"event_type":"step.start"}`), &param)
	if len(start) != 0 {
		t.Fatalf("start output count = %d, want 0", len(start))
	}
	delta := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"index":0,"delta":{"type":"arguments_delta","arguments":"{\"location\":\"北京\"}"},"event_type":"step.delta"}`), &param)
	if len(delta) != 1 {
		t.Fatalf("delta output count = %d, want 1", len(delta))
	}
	if got := gjson.GetBytes(delta[0], "candidates.0.content.parts.0.functionCall.name").String(); got != "get_weather" {
		t.Fatalf("functionCall.name = %q, want get_weather. Payload: %s", got, string(delta[0]))
	}
	if got := gjson.GetBytes(delta[0], "candidates.0.content.parts.0.functionCall.args.location").String(); got != "北京" {
		t.Fatalf("functionCall.args.location = %q, want 北京. Payload: %s", got, string(delta[0]))
	}
	if got := gjson.GetBytes(delta[0], "candidates.0.content.parts.0.functionCall.id").String(); got != "call_1" {
		t.Fatalf("functionCall.id = %q, want call_1. Payload: %s", got, string(delta[0]))
	}
	if got := gjson.GetBytes(delta[0], "candidates.0.content.parts.0.thoughtSignature").String(); got != "sig_1" {
		t.Fatalf("thoughtSignature = %q, want sig_1. Payload: %s", got, string(delta[0]))
	}
	completed := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`data: {"interaction":{"id":"i1","status":"requires_action","usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5,"total_thought_tokens":1,"total_cached_tokens":4},"service_tier":"standard","model":"gemini-3.1-flash-lite"},"event_type":"interaction.completed"}`), &param)
	if len(completed) != 1 {
		t.Fatalf("completed output count = %d, want 1", len(completed))
	}
	if got := gjson.GetBytes(completed[0], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP. Payload: %s", got, string(completed[0]))
	}
	if got := gjson.GetBytes(completed[0], "usageMetadata.promptTokenCount").Int(); got != 2 {
		t.Fatalf("promptTokenCount = %d, want 2. Payload: %s", got, string(completed[0]))
	}
	if got := gjson.GetBytes(completed[0], "usageMetadata.candidatesTokenCount").Int(); got != 3 {
		t.Fatalf("candidatesTokenCount = %d, want 3. Payload: %s", got, string(completed[0]))
	}
	if got := gjson.GetBytes(completed[0], "usageMetadata.totalTokenCount").Int(); got != 5 {
		t.Fatalf("totalTokenCount = %d, want 5. Payload: %s", got, string(completed[0]))
	}
	if got := gjson.GetBytes(completed[0], "usageMetadata.promptTokensDetails.0.tokenCount").Int(); got != 2 {
		t.Fatalf("promptTokensDetails.0.tokenCount = %d, want 2. Payload: %s", got, string(completed[0]))
	}
	done := ConvertInteractionsResponseToGemini(context.Background(), "gemini-3.1-flash-lite", nil, nil, []byte(`event: done
data: [DONE]`), &param)
	if len(done) != 0 {
		t.Fatalf("done output count = %d, want 0", len(done))
	}
}

func TestConvertInteractionsResponseToGeminiStreamFinishMetadataUsage(t *testing.T) {
	var param any
	out := ConvertInteractionsResponseToGemini(context.Background(), "gemini-test", nil, nil, []byte(`data: {"event_type":"finish","metadata":{"total_usage":{"total_input_tokens":2,"total_output_tokens":6,"total_thought_tokens":3,"total_cached_tokens":1,"total_tokens":11}}}`), &param)
	if len(out) != 1 {
		t.Fatalf("output count = %d, want 1", len(out))
	}
	if got := gjson.GetBytes(out[0], "candidates.0.finishReason").String(); got != "STOP" {
		t.Fatalf("finishReason = %q, want STOP. Payload: %s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "usageMetadata.promptTokenCount").Int(); got != 2 {
		t.Fatalf("promptTokenCount = %d, want 2. Payload: %s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "usageMetadata.candidatesTokenCount").Int(); got != 6 {
		t.Fatalf("candidatesTokenCount = %d, want 6. Payload: %s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "usageMetadata.thoughtsTokenCount").Int(); got != 3 {
		t.Fatalf("thoughtsTokenCount = %d, want 3. Payload: %s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "usageMetadata.cachedContentTokenCount").Int(); got != 1 {
		t.Fatalf("cachedContentTokenCount = %d, want 1. Payload: %s", got, string(out[0]))
	}
	if got := gjson.GetBytes(out[0], "usageMetadata.totalTokenCount").Int(); got != 11 {
		t.Fatalf("totalTokenCount = %d, want 11. Payload: %s", got, string(out[0]))
	}
}

func TestConvertInteractionsResponseToGeminiNonStreamFunctionCall(t *testing.T) {
	raw := []byte(`{"id":"i1","model":"gemini-3.1-flash-lite","steps":[{"type":"function_call","call_id":"call_1","signature":"sig_1","name":"get_weather","arguments":{"location":"北京"}}],"usage":{"total_input_tokens":2,"total_output_tokens":3,"total_tokens":5}}`)
	out := ConvertInteractionsResponseToGeminiNonStream(context.Background(), "gemini-3.1-flash-lite", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.name").String(); got != "get_weather" {
		t.Fatalf("functionCall.name = %q, want get_weather. Payload: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.args.location").String(); got != "北京" {
		t.Fatalf("functionCall.args.location = %q, want 北京. Payload: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.thoughtSignature").String(); got != "sig_1" {
		t.Fatalf("thoughtSignature = %q, want sig_1. Payload: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usageMetadata.totalTokenCount").Int(); got != 5 {
		t.Fatalf("totalTokenCount = %d, want 5. Payload: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiTurnInput(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"user","steps":[{"type":"user_input","content":[{"text":"hi"}]}]}}`), false)
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("text = %q, want hi", got)
	}
}

func TestConvertInteractionsRequestToGeminiTurnArrayInput(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"role":"user","steps":[{"type":"user_input","content":[{"text":"hi"}]}]},{"role":"assistant","steps":[{"type":"model_output","content":[{"text":"ok"}]}]}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.1.parts.0.text").String(); got != "ok" {
		t.Fatalf("contents.1.parts.0.text = %q, want ok. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiPreservesExpressibleTopLevelFields(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","tool_choice":{"type":"function","function":{"name":"lookup"}},"response_modalities":["text","image"],"service_tier":"priority","input":"hi"}`), false)
	if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("toolConfig.functionCallingConfig.mode = %q, want ANY. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != "lookup" {
		t.Fatalf("allowedFunctionNames.0 = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.responseModalities.0").String(); got != "TEXT" {
		t.Fatalf("responseModalities.0 = %q, want TEXT. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.responseModalities.1").String(); got != "IMAGE" {
		t.Fatalf("responseModalities.1 = %q, want IMAGE. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want priority. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiContentInput(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"user","parts":[{"text":"hi"}]}}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi", got)
	}
}

func TestConvertInteractionsRequestToGeminiContentArrayInput(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"role":"user","parts":[{"text":"hi"}]},{"role":"assistant","parts":[{"text":"ok"}]}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "user" {
		t.Fatalf("contents.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("contents.0.parts.0.text = %q, want hi", got)
	}
	if got := gjson.GetBytes(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model", got)
	}
	if got := gjson.GetBytes(out, "contents.1.parts.0.text").String(); got != "ok" {
		t.Fatalf("contents.1.parts.0.text = %q, want ok", got)
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamFunctionCall(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3,"cachedContentTokenCount":4}}`))
	if got := gjson.GetBytes(out, "steps.0.type").String(); got != "function_call" {
		t.Fatalf("step type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(out, "steps.0.name").String(); got != "lookup" {
		t.Fatalf("name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "usage.cached_tokens").Int(); got != 4 {
		t.Fatalf("cached tokens = %d, want 4", got)
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamFunctionCallPreservesCallID(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_response_1","args":{"q":"x"}}}]}}]}`))
	if got := gjson.GetBytes(out, "steps.0.call_id").String(); got != "call_response_1" {
		t.Fatalf("steps.0.call_id = %q, want call_response_1", got)
	}
}

func TestConvertGeminiResponseToInteractionsStreamFunctionCallCallID(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_stream_1","args":{"q":"x"}}}]}}]}`), &param)
	payload := findStepDeltaPayload(out)
	if len(payload) == 0 {
		t.Fatalf("step.delta payload not found")
	}
	startPayload := findEventPayload(out, "step.start")
	if got := gjson.GetBytes(startPayload, "step.id").String(); got != "call_stream_1" {
		t.Fatalf("step.id = %q, want call_stream_1", got)
	}
	if got := gjson.GetBytes(payload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string", got)
	}
}

func TestConvertGeminiResponseToInteractionsStreamFunctionCallThoughtSignature(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}]}`), &param)
	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"I will call the tool."}]}}]}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thoughtSignature":"sig-call","functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]}}]}`), &param)

	out := append(append(thoughtOut, textOut...), callOut...)
	signaturePayload := findStepDeltaPayloadByType(out, "thought_signature")
	if len(signaturePayload) == 0 {
		t.Fatalf("thought_signature step.delta payload not found. Events: %s", eventTypes(out))
	}
	if got := gjson.GetBytes(signaturePayload, "delta.signature").String(); got != "sig-call" {
		t.Fatalf("delta.signature = %q, want sig-call. Payload: %s", got, string(signaturePayload))
	}
	if got := gjson.GetBytes(signaturePayload, "index").Int(); got != 2 {
		t.Fatalf("signature index = %d, want 2. Events: %s", got, eventTypes(out))
	}
	functionStartPayload := findNthEventPayload(out, "step.start", 3)
	if got := gjson.GetBytes(functionStartPayload, "step.type").String(); got != "function_call" {
		t.Fatalf("fourth step type = %q, want function_call. Events: %s", got, eventTypes(out))
	}
	if got := gjson.GetBytes(functionStartPayload, "step.id").String(); got != "call_1" {
		t.Fatalf("function call id = %q, want call_1. Payload: %s", got, string(functionStartPayload))
	}
	argumentsPayload := findStepDeltaPayloadByType(out, "arguments_delta")
	if got := gjson.GetBytes(argumentsPayload, "delta.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("delta.arguments = %q, want JSON string. Payload: %s", got, string(argumentsPayload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamStepLifecycle(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}]}`), &param)
	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"answer"}]}}]}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7,"thoughtsTokenCount":2}}`), &param)

	out := append(append(thoughtOut, textOut...), callOut...)
	if got := eventTypes(out); !bytes.Equal(got, []byte("interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed")) {
		t.Fatalf("event sequence = %s", got)
	}
	if got := gjson.GetBytes(findNthEventPayload(out, "step.start", 0), "step.type").String(); got != "thought" {
		t.Fatalf("first step type = %q, want thought", got)
	}
	if got := gjson.GetBytes(findNthEventPayload(out, "step.start", 1), "step.type").String(); got != "model_output" {
		t.Fatalf("second step type = %q, want model_output", got)
	}
	if got := gjson.GetBytes(findNthEventPayload(out, "step.start", 2), "step.type").String(); got != "function_call" {
		t.Fatalf("third step type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(findNthEventPayload(out, "step.delta", 0), "delta.type").String(); got != "thought_summary" {
		t.Fatalf("thought delta type = %q, want thought_summary", got)
	}
	if got := gjson.GetBytes(findNthEventPayload(out, "step.delta", 2), "delta.type").String(); got != "arguments_delta" {
		t.Fatalf("function delta type = %q, want arguments_delta", got)
	}
	completed := findCompletedPayload(out)
	if got := gjson.GetBytes(completed, "interaction.usage.total_input_tokens").Int(); got != 3 {
		t.Fatalf("total_input_tokens = %d, want 3. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "interaction.usage.total_output_tokens").Int(); got != 4 {
		t.Fatalf("total_output_tokens = %d, want 4. Payload: %s", got, string(completed))
	}
	if got := gjson.GetBytes(completed, "interaction.usage.total_thought_tokens").Int(); got != 2 {
		t.Fatalf("total_thought_tokens = %d, want 2. Payload: %s", got, string(completed))
	}
}

func TestConvertGeminiResponseToInteractionsStreamEmitsTerminalOnce(t *testing.T) {
	var param any
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"finishReason":"STOP"}]}`), &param)
	usageOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`), &param)
	doneOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`[DONE]`), &param)

	if got := countEventType(finishOut, "step.stop"); got != 0 {
		t.Fatalf("finish step.stop count = %d, want 0", got)
	}
	if got := countEventType(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(usageOut, "step.stop"); got != 0 {
		t.Fatalf("usage step.stop count = %d, want 0", got)
	}
	if got := countEventType(usageOut, "interaction.completed"); got != 1 {
		t.Fatalf("usage interaction.completed count = %d, want 1", got)
	}
	if got := countEventType(doneOut, "interaction.completed"); got != 0 {
		t.Fatalf("done interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(doneOut, "done"); got != 1 {
		t.Fatalf("done event count = %d, want 1", got)
	}
	if payload := findEventPayload(doneOut, "done"); string(payload) != "[DONE]" {
		t.Fatalf("done payload = %q, want [DONE]", string(payload))
	}
	payload := findCompletedPayload(usageOut)
	if got := gjson.GetBytes(payload, "interaction.usage.total_tokens").Int(); got != 3 {
		t.Fatalf("completed total_tokens = %d, want 3. Payload: %s", got, string(payload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamDoesNotCompleteOnNonTerminalUsage(t *testing.T) {
	var param any
	thoughtOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]}}],"usageMetadata":{"promptTokenCount":124,"totalTokenCount":124}}`), &param)
	if got := countEventType(thoughtOut, "interaction.completed"); got != 0 {
		t.Fatalf("thought interaction.completed count = %d, want 0. Events: %s", got, eventTypes(thoughtOut))
	}
	if got := countEventType(thoughtOut, "step.stop"); got != 0 {
		t.Fatalf("thought step.stop count = %d, want 0. Events: %s", got, eventTypes(thoughtOut))
	}

	textOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"好的，我将为您调用天气查询工具。"}]}}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":17,"totalTokenCount":452,"thoughtsTokenCount":311}}`), &param)
	callOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"location":"北京"},"id":"nriii75p"}}]}}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":33,"totalTokenCount":468,"thoughtsTokenCount":311}}`), &param)
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash-low", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":124,"candidatesTokenCount":33,"totalTokenCount":468,"thoughtsTokenCount":311}}`), &param)

	out := append(append(append(thoughtOut, textOut...), callOut...), finishOut...)
	if got := countEventType(out, "interaction.completed"); got != 1 {
		t.Fatalf("interaction.completed count = %d, want 1. Events: %s", got, eventTypes(out))
	}
	if got := eventTypes(out); !bytes.Equal(got, []byte("interaction.created,interaction.status_update,step.start,step.delta,step.stop,step.start,step.delta,step.stop,step.start,step.delta,step.stop,interaction.completed")) {
		t.Fatalf("event sequence = %s", got)
	}
	payload := findCompletedPayload(out)
	if got := gjson.GetBytes(payload, "interaction.usage.total_tokens").Int(); got != 468 {
		t.Fatalf("completed total_tokens = %d, want 468. Payload: %s", got, string(payload))
	}
}

func TestConvertGeminiResponseToInteractionsStreamIgnoresTrafficOnlyUsageMetadata(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"content":{"role":"model","parts":[]}}],"usageMetadata":{"trafficType":"PROVISIONED_THROUGHPUT"}}`), &param)
	if got := countEventType(out, "interaction.completed"); got != 0 {
		t.Fatalf("interaction.completed count = %d, want 0. Events: %q", got, out)
	}
	if got := countEventType(out, "done"); got != 0 {
		t.Fatalf("done count = %d, want 0. Events: %q", got, out)
	}
}

func TestConvertGeminiResponseToInteractionsStreamCompletesOnDoneWithoutUsage(t *testing.T) {
	var param any
	finishOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`{"candidates":[{"finishReason":"STOP"}]}`), &param)
	doneOut := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.5-flash", nil, nil, []byte(`[DONE]`), &param)

	if got := countEventType(finishOut, "interaction.completed"); got != 0 {
		t.Fatalf("finish interaction.completed count = %d, want 0", got)
	}
	if got := countEventType(doneOut, "interaction.completed"); got != 1 {
		t.Fatalf("done interaction.completed count = %d, want 1", got)
	}
	if got := countEventType(doneOut, "done"); got != 1 {
		t.Fatalf("done event count = %d, want 1", got)
	}
}

func TestConvertInteractionsRequestToGeminiImageContent(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"user_input","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="}]}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("mimeType = %q, want image/png", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.inlineData.data").String(); got != "aGVsbG8=" {
		t.Fatalf("data = %q, want aGVsbG8=", got)
	}
}

func TestConvertInteractionsRequestToGeminiModelOutputTypedContent(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"model_output","content":[{"type":"image","mime_type":"image/png","data":"aGVsbG8="},{"type":"document","mime_type":"application/pdf","file_uri":"gs://bucket/doc.pdf"}]}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "model" {
		t.Fatalf("contents.0.role = %q, want model. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.inlineData.mimeType").String(); got != "image/png" {
		t.Fatalf("image mimeType = %q, want image/png. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.inlineData.data").String(); got != "aGVsbG8=" {
		t.Fatalf("image data = %q, want aGVsbG8=. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.1.fileData.mimeType").String(); got != "application/pdf" {
		t.Fatalf("document mimeType = %q, want application/pdf. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.1.fileData.fileUri").String(); got != "gs://bucket/doc.pdf" {
		t.Fatalf("document fileUri = %q, want gs://bucket/doc.pdf. Output: %s", got, string(out))
	}
}

func TestConvertInteractionsRequestToGeminiThoughtTypedContent(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"thought","content":[{"type":"text","text":"thinking"},{"type":"audio","mime_type":"audio/wav","data":"UklGRg=="}]}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.parts.0.text").String(); got != "thinking" {
		t.Fatalf("thought text = %q, want thinking. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.thought").Bool(); !got {
		t.Fatalf("thought flag = false, want true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "contents.0.parts.1.inlineData.mimeType").String(); got != "audio/wav" {
		t.Fatalf("audio mimeType = %q, want audio/wav. Output: %s", got, string(out))
	}
}

func TestConvertGeminiResponseToInteractionsNonStreamImage(t *testing.T) {
	out := convertGeminiResponseToInteractionsNonStreamDirect("gemini-3.5-flash", nil, nil, []byte(`{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]}`))
	if got := gjson.GetBytes(out, "steps.0.content.0.type").String(); got != "image" {
		t.Fatalf("content type = %q, want image", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigAllFields(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"max_output_tokens":32,"response_schema":{"type":"object"},"seed":42,"thinking_config":{"thinking_budget":1024,"include_thoughts":true},"context_window_compression":{"trigger_tokens":1000}},"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.responseSchema.type").String(); got != "object" {
		t.Fatalf("responseSchema.type = %q, want object", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.seed").Int(); got != 42 {
		t.Fatalf("seed = %d, want 42", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 1024 {
		t.Fatalf("thinkingBudget = %d, want 1024", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("includeThoughts = false, want true")
	}
	if got := gjson.GetBytes(out, "generationConfig.contextWindowCompression.triggerTokens").Int(); got != 1000 {
		t.Fatalf("triggerTokens = %d, want 1000", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigProtocolFields(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"stream":true,"input":"hi"}`), true)
	for _, path := range []string{
		"stream",
		"generationConfig.toolChoice",
		"generationConfig.thinkingLevel",
		"generationConfig.thinkingSummaries",
	} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s exists, want omitted. Output: %s", path, string(out))
		}
	}
	if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("toolConfig.functionCallingConfig.mode = %q, want AUTO. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("thinkingLevel = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("includeThoughts = false, want true. Output: %s", string(out))
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCall(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"ok":true}}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "function_call" {
		t.Fatalf("input.0.type = %q, want function_call", got)
	}
	if got := gjson.GetBytes(out, "input.0.name").String(); got != "lookup" {
		t.Fatalf("input.0.name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "input.1.type").String(); got != "function_result" {
		t.Fatalf("input.1.type = %q, want function_result", got)
	}
}

func TestConvertGeminiRequestToInteractionsTextContentType(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "input.0.content.0.text").String(); got != "hi" {
		t.Fatalf("content.0.text = %q, want hi. Output: %s", got, string(out))
	}
}

func TestConvertGeminiRequestToInteractionsMultimodal(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"audio/wav","data":"aGVsbG8="}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "user_input" {
		t.Fatalf("input.0.type = %q, want user_input", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.type").String(); got != "audio" {
		t.Fatalf("content.0.type = %q, want audio", got)
	}
	if got := gjson.GetBytes(out, "input.0.content.0.mime_type").String(); got != "audio/wav" {
		t.Fatalf("mime_type = %q, want audio/wav", got)
	}
}

func TestConvertGeminiRequestToInteractionsThought(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"text":"thinking","thought":true}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "thought" {
		t.Fatalf("input.0.type = %q, want thought", got)
	}
}

func TestConvertInteractionsRequestToGeminiTurnWithModelRole(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":{"role":"model","steps":[{"type":"user_input","content":[{"text":"hi"}]},{"type":"model_output","content":[{"text":"ok"}]}]}}`), false)
	if got := gjson.GetBytes(out, "contents.0.role").String(); got != "model" {
		t.Fatalf("contents.0.role = %q, want model", got)
	}
	if got := gjson.GetBytes(out, "contents.1.role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model", got)
	}
}

func TestConvertInteractionsRequestToGeminiGenerationConfigPreservesLargeIntegers(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generation_config":{"max_output_tokens":32,"large_identity":9223372036854775807},"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens = %d, want 32", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.largeIdentity").String(); got != "9223372036854775807" {
		t.Fatalf("largeIdentity = %q, want 9223372036854775807", got)
	}
}

func TestConvertInteractionsRequestToGeminiFunctionCallPreservesCallID(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionCall.id").String(); got != "call_1" {
		t.Fatalf("functionCall.id = %q, want call_1", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertInteractionsRequestToGeminiFunctionResultPreservesCallID(t *testing.T) {
	out := ConvertInteractionsRequestToGemini("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","input":[{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}]}`), false)
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionResponse.id").String(); got != "call_1" {
		t.Fatalf("functionResponse.id = %q, want call_1", got)
	}
	if got := gjson.GetBytes(out, "contents.0.parts.0.functionResponse.name").String(); got != "lookup" {
		t.Fatalf("functionResponse.name = %q, want lookup", got)
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCallPreservesID(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","id":"call_1","response":{"ok":true}}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "call_1" {
		t.Fatalf("input.0.call_id = %q, want call_1", got)
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "call_1" {
		t.Fatalf("input.1.call_id = %q, want call_1", got)
	}
}

func TestConvertGeminiRequestToInteractionsFunctionCallPreservesCallID(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","call_id":"call_request_1","args":{"q":"x"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","call_id":"call_request_1","response":{"ok":true}}}]}]}`), false)
	if got := gjson.GetBytes(out, "input.0.call_id").String(); got != "call_request_1" {
		t.Fatalf("input.0.call_id = %q, want call_request_1", got)
	}
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "call_request_1" {
		t.Fatalf("input.1.call_id = %q, want call_request_1", got)
	}
}

func TestConvertGeminiRequestToInteractionsGenerationConfig(t *testing.T) {
	out := ConvertGeminiRequestToInteractions("gemini-3.5-flash", []byte(`{"model":"gemini-3.5-flash","generationConfig":{"maxOutputTokens":32,"topP":0.8,"thinkingConfig":{"thinkingBudget":1024}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), false)
	if got := gjson.GetBytes(out, "generation_config.max_output_tokens").Int(); got != 32 {
		t.Fatalf("max_output_tokens = %d, want 32", got)
	}
	if got := gjson.GetBytes(out, "generation_config.top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
	if got := gjson.GetBytes(out, "generation_config.thinking_config.thinking_budget").Int(); got != 1024 {
		t.Fatalf("thinking_budget = %d, want 1024", got)
	}
}

func findStepDeltaPayload(events [][]byte) []byte {
	return findEventPayload(events, "step.delta")
}

func findStepDeltaPayloadByType(events [][]byte, deltaType string) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == "step.delta" && gjson.GetBytes(payload, "delta.type").String() == deltaType {
			return payload
		}
	}
	return nil
}

func findCompletedPayload(events [][]byte) []byte {
	return findEventPayload(events, "interaction.completed")
}

func findEventPayload(events [][]byte, eventType string) []byte {
	return findNthEventPayload(events, eventType, 0)
}

func findNthEventPayload(events [][]byte, eventType string, n int) []byte {
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == eventType {
			if n == 0 {
				return payload
			}
			n--
		}
	}
	return nil
}

func eventTypes(events [][]byte) []byte {
	var out []byte
	for _, event := range events {
		payload := ssePayload(event)
		eventType := eventName(event, payload)
		if eventType == "" {
			continue
		}
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, eventType...)
	}
	return out
}

func countEventType(events [][]byte, eventType string) int {
	count := 0
	for _, event := range events {
		payload := ssePayload(event)
		if eventName(event, payload) == eventType {
			count++
		}
	}
	return count
}

func eventName(event, payload []byte) string {
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

func ssePayload(event []byte) []byte {
	const prefix = "\ndata: "
	idx := bytes.Index(event, []byte(prefix))
	if idx < 0 {
		return nil
	}
	return event[idx+len(prefix):]
}
