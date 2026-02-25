package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAIResponses(t *testing.T) {
	ctx := context.Background()
	var param any

	// Message start
	raw := []byte(`data: {"type": "message_start", "message": {"id": "msg_123", "role": "assistant", "model": "claude-3"}}`)
	got := ConvertClaudeResponseToOpenAIResponses(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(got))
	}

	// Content block start (text)
	raw = []byte(`data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}`)
	got = ConvertClaudeResponseToOpenAIResponses(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(got))
	}

	// Content delta
	raw = []byte(`data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello"}}`)
	got = ConvertClaudeResponseToOpenAIResponses(ctx, "gpt-4o", nil, nil, raw, &param)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}

	// Message stop
	raw = []byte(`data: {"type": "message_stop"}`)
	got = ConvertClaudeResponseToOpenAIResponses(ctx, "gpt-4o", nil, []byte(`{"model": "gpt-4o"}`), raw, &param)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}
	res := gjson.Parse(got[0][strings.Index(got[0], "data: ")+6:])
	if res.Get("type").String() != "response.completed" {
		t.Errorf("expected response.completed, got %s", res.Get("type").String())
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream(t *testing.T) {
	raw := []byte(`data: {"type": "message_start", "message": {"id": "msg_123", "model": "claude-3"}}
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello "}}
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "world"}}
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 10, "output_tokens": 5}}`)

	got := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "gpt-4o", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("status").String() != "completed" {
		t.Errorf("expected completed, got %s", res.Get("status").String())
	}
	output := res.Get("output").Array()
	if len(output) == 0 || output[0].Get("content.0.text").String() != "hello world" {
		t.Errorf("unexpected content: %s", got)
	}
}
