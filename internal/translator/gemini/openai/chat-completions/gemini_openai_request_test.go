package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGemini_NormalizesImageVariants(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"compare"},
					{"type":"image_url","image_url":"data:image/png;base64,AAAA"},
					{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"BBBB"}}
				]
			}
		]
	}`)

	out := ConvertOpenAIRequestToGemini("gemini-test", input, false)
	parts := gjson.GetBytes(out, "contents.0.parts")
	if got := parts.Get("0.text").String(); got != "compare" {
		t.Fatalf("expected text part, got %q: %s", got, string(out))
	}
	if got := parts.Get("1.inlineData.data").String(); got != "AAAA" {
		t.Fatalf("expected first image data, got %q: %s", got, string(out))
	}
	if got := parts.Get("1.inlineData.mime_type").String(); got != "image/png" {
		t.Fatalf("expected first image mime type, got %q: %s", got, string(out))
	}
	if got := parts.Get("2.inlineData.data").String(); got != "BBBB" {
		t.Fatalf("expected second image data, got %q: %s", got, string(out))
	}
	if got := parts.Get("2.inlineData.mime_type").String(); got != "image/jpeg" {
		t.Fatalf("expected second image mime type, got %q: %s", got, string(out))
	}
}
