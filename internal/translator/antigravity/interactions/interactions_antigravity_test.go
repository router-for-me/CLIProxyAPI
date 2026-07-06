package interactions

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertInteractionsRequestToAntigravityWithToolMessagesDirect(t *testing.T) {
	out := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","system_instruction":"be brief","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"function_call","name":"lookup","call_id":"call_1","arguments":{"q":"x"}},{"type":"function_result","name":"lookup","call_id":"call_1","result":{"ok":true}}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]}`), false)
	if got := gjson.GetBytes(out, "request.systemInstruction.parts.0.text").String(); got != "be brief" {
		t.Fatalf("request.systemInstruction.parts.0.text = %q, want be brief. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String(); got != "lookup" {
		t.Fatalf("functionCall.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String(); got != "lookup" {
		t.Fatalf("functionResponse.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.name").String(); got != "lookup" {
		t.Fatalf("request.tools.0.functionDeclarations.0.name = %q, want lookup. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.q.type").String(); got != "string" {
		t.Fatalf("tool parameters schema was not preserved. Output: %s", string(out))
	}
}

func TestConvertInteractionsRequestToAntigravityPreservesGenerationConfig(t *testing.T) {
	out := ConvertInteractionsRequestToAntigravity("antigravity-test", []byte(`{"model":"antigravity-test","input":"hi","generation_config":{"max_output_tokens":16,"top_p":0.8,"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"reasoning":{"summary":"auto"},"stream":true}`), true)
	if gjson.GetBytes(out, "input").Exists() {
		t.Fatalf("raw interactions input exists in translated request. Output: %s", string(out))
	}
	for _, path := range []string{
		"request.generationConfig.toolChoice",
		"request.generationConfig.thinkingLevel",
		"request.generationConfig.thinkingSummaries",
	} {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("%s exists, want omitted. Output: %s", path, string(out))
		}
	}
	if got := gjson.GetBytes(out, "request.stream").Bool(); !got {
		t.Fatalf("request.stream = false, want true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 16 {
		t.Fatalf("request.generationConfig.maxOutputTokens = %d, want 16. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.topP").Float(); got != 0.8 {
		t.Fatalf("request.generationConfig.topP = %v, want 0.8. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("request.generationConfig.thinkingConfig.thinkingLevel = %q, want high. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("request.generationConfig.thinkingConfig.includeThoughts = false, want true. Output: %s", string(out))
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("request.toolConfig.functionCallingConfig.mode = %q, want AUTO. Output: %s", got, string(out))
	}
}

func TestConvertAntigravityResponseToInteractionsNonStream(t *testing.T) {
	raw := []byte(`{"response":{"responseId":"resp_1","candidates":[{"content":{"role":"model","parts":[{"text":"ok"},{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}}`)
	out := ConvertAntigravityResponseToInteractionsNonStream(context.Background(), "antigravity-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "steps.0.content.0.text").String(); got != "ok" {
		t.Fatalf("steps.0.content.0.text = %q, want ok. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "steps.1.type").String(); got != "function_call" {
		t.Fatalf("steps.1.type = %q, want function_call. Output: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 5 {
		t.Fatalf("usage.total_tokens = %d, want 5. Output: %s", got, string(out))
	}
}

func TestConvertAntigravityResponseToInteractionsStream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "alt", "")
	var param any
	events := ConvertAntigravityResponseToInteractions(ctx, "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}}`), &param)
	payload := findAntigravityInteractionsEventPayload(events, "step.delta")
	if len(payload) == 0 {
		t.Fatalf("step.delta event not found: %q", events)
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "ok" {
		t.Fatalf("delta.text = %q, want ok. Payload: %s", got, string(payload))
	}
}

func TestConvertAntigravityResponseToInteractionsStreamFunctionCallStartHasCallID(t *testing.T) {
	var param any
	events := ConvertAntigravityResponseToInteractions(context.Background(), "antigravity-test", nil, nil, []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"lookup","id":"call_1","args":{"q":"x"}}}]}}]}}`), &param)
	payload := findAntigravityInteractionsEventPayload(events, "step.start")
	if got := gjson.GetBytes(payload, "step.call_id").String(); got != "call_1" {
		t.Fatalf("step.call_id = %q, want call_1. Payload: %s", got, string(payload))
	}
}

func findAntigravityInteractionsEventPayload(events [][]byte, eventType string) []byte {
	prefix := []byte("data:")
	for _, event := range events {
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, prefix) {
				continue
			}
			payload := bytes.TrimSpace(line[len(prefix):])
			if gjson.GetBytes(payload, "type").String() == eventType || gjson.GetBytes(payload, "event_type").String() == eventType {
				return payload
			}
		}
	}
	return nil
}
