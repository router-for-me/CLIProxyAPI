package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToOpenAINormalizesImageVariants(t *testing.T) {
	input := []byte(`{
		"model":"placeholder",
		"messages":[{"role":"user","content":[
			{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"high"},
			{"type":"image_url","image_url":"data:image/jpeg;base64,BBBB"}
		]}]
	}`)

	out := ConvertOpenAIRequestToOpenAI("kimi-latest", input, false)

	if got := gjson.GetBytes(out, "model").String(); got != "kimi-latest" {
		t.Fatalf("model = %q, want kimi-latest: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.type").String(); got != "image_url" {
		t.Fatalf("first part type = %q, want image_url: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("first image URL = %q, want data URL: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.detail").String(); got != "high" {
		t.Fatalf("first image detail = %q, want high: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.image_url.url").String(); got != "data:image/jpeg;base64,BBBB" {
		t.Fatalf("second image URL = %q, want data URL: %s", got, string(out))
	}
}
