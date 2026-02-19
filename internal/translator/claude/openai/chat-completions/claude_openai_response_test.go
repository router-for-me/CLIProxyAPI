package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAI(t *testing.T) {
	ctx := context.Background()
	model := "gpt-4o"
	var param any
	
	// Message start
	raw := []byte(`data: {"type": "message_start", "message": {"id": "msg_123", "role": "assistant", "model": "claude-3"}}`)
	got := ConvertClaudeResponseToOpenAI(ctx, model, nil, nil, raw, &param)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}
	res := gjson.Parse(got[0])
	if res.Get("id").String() != "msg_123" || res.Get("choices.0.delta.role").String() != "assistant" {
		t.Errorf("unexpected message_start output: %s", got[0])
	}
	
	// Content delta
	raw = []byte(`data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello"}}`)
	got = ConvertClaudeResponseToOpenAI(ctx, model, nil, nil, raw, &param)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}
	res = gjson.Parse(got[0])
	if res.Get("choices.0.delta.content").String() != "hello" {
		t.Errorf("unexpected content_block_delta output: %s", got[0])
	}
	
	// Message delta (usage)
	raw = []byte(`data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 10, "output_tokens": 5}}`)
	got = ConvertClaudeResponseToOpenAI(ctx, model, nil, nil, raw, &param)
	if len(got) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(got))
	}
	res = gjson.Parse(got[0])
	if res.Get("usage.total_tokens").Int() != 15 {
		t.Errorf("unexpected usage output: %s", got[0])
	}
}

func TestConvertClaudeResponseToOpenAINonStream(t *testing.T) {
	raw := []byte(`data: {"type": "message_start", "message": {"id": "msg_123", "model": "claude-3"}}
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "hello "}}
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "world"}}
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn"}, "usage": {"input_tokens": 10, "output_tokens": 5}}`)
	
	got := ConvertClaudeResponseToOpenAINonStream(context.Background(), "gpt-4o", nil, nil, raw, nil)
	res := gjson.Parse(got)
	if res.Get("choices.0.message.content").String() != "hello world" {
		t.Errorf("unexpected content: %s", got)
	}
	if res.Get("usage.total_tokens").Int() != 15 {
		t.Errorf("unexpected usage: %s", got)
	}
}
