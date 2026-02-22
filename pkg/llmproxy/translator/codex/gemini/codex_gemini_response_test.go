package gemini

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToGemini(t *testing.T) {
	ctx := context.Background()
	var param any

	// response.created
	raw := []byte(`data: {"type": "response.created", "response": {"id": "resp_123", "model": "gpt-4o"}}`)
	got := ConvertCodexResponseToGemini(ctx, "gemini-1.5-pro", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	res := gjson.Parse(got[0])
	if res.Get("responseId").String() != "resp_123" {
		t.Errorf("unexpected output: %s", got[0])
	}

	// response.output_text.delta
	raw = []byte(`data: {"type": "response.output_text.delta", "delta": "hello"}`)
	got = ConvertCodexResponseToGemini(ctx, "gemini-1.5-pro", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	res = gjson.Parse(got[0])
	if res.Get("candidates.0.content.parts.0.text").String() != "hello" {
		t.Errorf("unexpected output: %s", got[0])
	}
}

func TestConvertCodexResponseToGeminiNonStream(t *testing.T) {
	raw := []byte(`{"type": "response.completed", "response": {
		"id": "resp_123",
		"model": "gpt-4o",
		"output": [
			{"type": "message", "content": [
				{"type": "output_text", "text": "hello"}
			]}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}}`)

	got := ConvertCodexResponseToGeminiNonStream(context.Background(), "gemini-1.5-pro", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("responseId").String() != "resp_123" {
		t.Errorf("expected id resp_123, got %s", res.Get("responseId").String())
	}
	if res.Get("candidates.0.content.parts.0.text").String() != "hello" {
		t.Errorf("unexpected content: %s", got)
	}
}
