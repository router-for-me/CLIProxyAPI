package chat_completions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToOpenAI_NonStreamToStandardChunks(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl-test",
		"object":"chat.completion",
		"created":1773161436,
		"model":"deepseek-v3.2",
		"usage":{"prompt_tokens":7,"completion_tokens":1,"total_tokens":8},
		"choices":[
			{
				"index":0,
				"finish_reason":"stop",
				"message":{"role":"assistant","content":"ok","tool_calls":[]}
			}
		]
	}`)

	got := ConvertOpenAIResponseToOpenAI(context.Background(), "deepseek-v3.2", nil, nil, raw, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(got))
	}

	first := gjson.ParseBytes(got[0])
	if first.Get("object").String() != "chat.completion.chunk" {
		t.Fatalf("first.object = %q", first.Get("object").String())
	}
	if first.Get("choices.0.delta.role").String() != "assistant" {
		t.Fatalf("first role = %q", first.Get("choices.0.delta.role").String())
	}
	if first.Get("choices.0.delta.content").String() != "ok" {
		t.Fatalf("first content = %q", first.Get("choices.0.delta.content").String())
	}

	second := gjson.ParseBytes(got[1])
	if second.Get("object").String() != "chat.completion.chunk" {
		t.Fatalf("second.object = %q", second.Get("object").String())
	}
	if second.Get("choices.0.finish_reason").String() != "stop" {
		t.Fatalf("second finish_reason = %q", second.Get("choices.0.finish_reason").String())
	}
	if second.Get("usage.total_tokens").Int() != 8 {
		t.Fatalf("second usage.total_tokens = %d", second.Get("usage.total_tokens").Int())
	}
}

func TestConvertOpenAIResponseToOpenAI_StreamChunkPassThrough(t *testing.T) {
	raw := []byte(`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"ok"}}]}`)
	got := ConvertOpenAIResponseToOpenAI(context.Background(), "deepseek-v3.2", nil, nil, raw, nil)
	if len(got) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got))
	}
	if string(got[0]) != string(raw) {
		t.Fatalf("expected passthrough, got %q", string(got[0]))
	}
}
