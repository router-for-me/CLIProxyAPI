package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertCodexResponseToClaude(t *testing.T) {
	ctx := context.Background()
	var param any

	// response.created
	raw := []byte(`data: {"type": "response.created", "response": {"id": "resp_123", "model": "gpt-4o"}}`)
	got := ConvertCodexResponseToClaude(ctx, "claude-3", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !strings.Contains(got[0], `"id":"resp_123"`) {
		t.Errorf("unexpected output: %s", got[0])
	}

	// response.output_text.delta
	raw = []byte(`data: {"type": "response.output_text.delta", "delta": "hello"}`)
	got = ConvertCodexResponseToClaude(ctx, "claude-3", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if !strings.Contains(got[0], `"text":"hello"`) {
		t.Errorf("unexpected output: %s", got[0])
	}
}

func TestConvertCodexResponseToClaudeNonStream(t *testing.T) {
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

	got := ConvertCodexResponseToClaudeNonStream(context.Background(), "claude-3", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("id").String() != "resp_123" {
		t.Errorf("expected id resp_123, got %s", res.Get("id").String())
	}
	if res.Get("content.0.text").String() != "hello" {
		t.Errorf("unexpected content: %s", got)
	}
}
