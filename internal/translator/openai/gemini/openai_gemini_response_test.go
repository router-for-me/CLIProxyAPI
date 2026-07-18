package gemini

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToGeminiNonStreamPreservesToolCallID(t *testing.T) {
	raw := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_chat_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`)
	out := ConvertOpenAIResponseToGeminiNonStream(context.Background(), "gpt-test", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.id").String(); got != "call_chat_1" {
		t.Fatalf("functionCall.id = %q, want call_chat_1", got)
	}
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}

func TestConvertOpenAIResponseToGeminiStreamPreservesToolCallID(t *testing.T) {
	var param any
	ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_stream_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]}}]}`), &param)
	out := ConvertOpenAIResponseToGemini(context.Background(), "gpt-test", nil, nil, []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`), &param)
	if len(out) == 0 {
		t.Fatalf("stream output is empty")
	}
	if got := gjson.GetBytes(out[len(out)-1], "candidates.0.content.parts.0.functionCall.id").String(); got != "call_stream_1" {
		t.Fatalf("functionCall.id = %q, want call_stream_1", got)
	}
	if got := gjson.GetBytes(out[len(out)-1], "candidates.0.content.parts.0.functionCall.args.q").String(); got != "x" {
		t.Fatalf("functionCall.args.q = %q, want x", got)
	}
}
