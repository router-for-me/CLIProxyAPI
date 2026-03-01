package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude_ToolMessagePreservesImageURL(t *testing.T) {
	input := `{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"snapshot"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-test", []byte(input), false)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want %q", got, "user")
	}
	if got := parsed.Get("messages.1.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.1.content.0.type = %q, want %q", got, "tool_result")
	}
	if got := parsed.Get("messages.1.content.0.tool_use_id").String(); got != "call_1" {
		t.Fatalf("tool_use_id = %q, want %q", got, "call_1")
	}
	if got := parsed.Get("messages.1.content.0.content.0.type").String(); got != "text" {
		t.Fatalf("first tool_result item type = %q, want %q", got, "text")
	}
	if got := parsed.Get("messages.1.content.0.content.0.text").String(); got != "snapshot" {
		t.Fatalf("first tool_result item text = %q, want %q", got, "snapshot")
	}
	if got := parsed.Get("messages.1.content.0.content.1.type").String(); got != "image" {
		t.Fatalf("second tool_result item type = %q, want %q", got, "image")
	}
	if got := parsed.Get("messages.1.content.0.content.1.source.type").String(); got != "url" {
		t.Fatalf("image source.type = %q, want %q", got, "url")
	}
	if got := parsed.Get("messages.1.content.0.content.1.source.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image source.url = %q, want %q", got, "https://example.com/a.png")
	}
}

func TestConvertOpenAIRequestToClaude_ToolMessagePreservesDataURLImage(t *testing.T) {
	input := `{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-test", []byte(input), false)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("messages.1.content.0.content.0.type").String(); got != "image" {
		t.Fatalf("tool_result image type = %q, want %q", got, "image")
	}
	if got := parsed.Get("messages.1.content.0.content.0.source.type").String(); got != "base64" {
		t.Fatalf("image source.type = %q, want %q", got, "base64")
	}
	if got := parsed.Get("messages.1.content.0.content.0.source.media_type").String(); got != "image/png" {
		t.Fatalf("image media_type = %q, want %q", got, "image/png")
	}
	if got := parsed.Get("messages.1.content.0.content.0.source.data").String(); got != "QUJD" {
		t.Fatalf("image data = %q, want %q", got, "QUJD")
	}
}

func TestConvertOpenAIRequestToClaude_ToolMessageStringUnchanged(t *testing.T) {
	input := `{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_image","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-test", []byte(input), false)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("messages.1.content.0.content").String(); got != "ok" {
		t.Fatalf("tool_result content = %q, want %q", got, "ok")
	}
}

func TestConvertOpenAIRequestToClaude_UserMessageConvertsDataURLImage(t *testing.T) {
	input := `{
		"model":"gpt-test",
		"messages":[
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;charset=utf-8;base64,QUJD"}}]}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-test", []byte(input), false)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("image type = %q, want %q", got, "image")
	}
	if got := parsed.Get("messages.0.content.0.source.type").String(); got != "base64" {
		t.Fatalf("image source.type = %q, want %q", got, "base64")
	}
	if got := parsed.Get("messages.0.content.0.source.media_type").String(); got != "image/png;charset=utf-8" {
		t.Fatalf("image source.media_type = %q, want %q", got, "image/png;charset=utf-8")
	}
	if got := parsed.Get("messages.0.content.0.source.data").String(); got != "QUJD" {
		t.Fatalf("image source.data = %q, want %q", got, "QUJD")
	}
}

func TestConvertOpenAIRequestToClaude_AssistantMessageConvertsImageURLString(t *testing.T) {
	input := `{
		"model":"gpt-test",
		"messages":[
			{"role":"assistant","content":[{"type":"image_url","image_url":"https://example.com/a.png"}]}
		]
	}`

	out := ConvertOpenAIRequestToClaude("claude-test", []byte(input), false)
	parsed := gjson.ParseBytes(out)

	if got := parsed.Get("messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("image type = %q, want %q", got, "image")
	}
	if got := parsed.Get("messages.0.content.0.source.type").String(); got != "url" {
		t.Fatalf("image source.type = %q, want %q", got, "url")
	}
	if got := parsed.Get("messages.0.content.0.source.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image source.url = %q, want %q", got, "https://example.com/a.png")
	}
}
