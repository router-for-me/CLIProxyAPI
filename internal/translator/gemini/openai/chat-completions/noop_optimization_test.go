package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiNormalizesToolNameAndStrict(t *testing.T) {
	input := []byte(`{"messages":[],"tools":[{"type":"function","function":{"name":true,"strict":true,"parameters":{"type":"object"}}}]}`)

	output := ConvertOpenAIRequestToGemini("gemini-test", input, false)

	name := gjson.GetBytes(output, "tools.0.functionDeclarations.0.name")
	if name.Type != gjson.String || name.String() != "true" {
		t.Fatalf("tool name = %s, want string true", name.Raw)
	}
	if gjson.GetBytes(output, "tools.0.functionDeclarations.0.strict").Exists() {
		t.Fatal("strict should be removed")
	}
}

func TestConvertGeminiResponseToOpenAINonStreamKeepsAssistantRole(t *testing.T) {
	input := []byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`)

	output := ConvertGeminiResponseToOpenAINonStream(context.Background(), "", nil, nil, input, nil)

	if role := gjson.GetBytes(output, "choices.0.message.role").String(); role != "assistant" {
		t.Fatalf("role = %q, want assistant", role)
	}
}

func TestConvertGeminiResponseToOpenAIStreamingSetsAssistantRoleOnce(t *testing.T) {
	input := []byte(`{"candidates":[{"index":0,"content":{"parts":[{"text":"hello"},{"functionCall":{"name":"lookup","args":{}}},{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}}]}`)
	var param any

	outputs := ConvertGeminiResponseToOpenAI(context.Background(), "", nil, nil, input, &param)

	if len(outputs) != 1 {
		t.Fatalf("output count = %d, want 1", len(outputs))
	}
	if role := gjson.GetBytes(outputs[0], "choices.0.delta.role").String(); role != "assistant" {
		t.Fatalf("role = %q, want assistant", role)
	}
	if got := gjson.GetBytes(outputs[0], "choices.0.delta.content").String(); got != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
	if !gjson.GetBytes(outputs[0], "choices.0.delta.tool_calls.0").Exists() {
		t.Fatal("tool call should be present")
	}
	if !gjson.GetBytes(outputs[0], "choices.0.delta.images.0").Exists() {
		t.Fatal("image should be present")
	}
}
