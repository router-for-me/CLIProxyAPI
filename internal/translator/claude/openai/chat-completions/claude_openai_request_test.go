package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude_KeepRemoteImageURL(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-5",
		"messages":[
			{
				"role":"user",
				"content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]
			}
		]
	}`)

	out := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", in, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.0.content.0.type").String(); got != "image" {
		t.Fatalf("image type mismatch: got=%q output=%s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.source.type").String(); got != "url" {
		t.Fatalf("image source type mismatch: got=%q output=%s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.source.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("image url mismatch: got=%q output=%s", got, string(out))
	}
}
