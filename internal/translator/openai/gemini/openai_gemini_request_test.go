package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI_InlineDataSpellingVariants(t *testing.T) {
	input := []byte(`{
		"contents":[
			{
				"role":"user",
				"parts":[
					{"text":"look"},
					{"inline_data":{"mime_type":"image/png","data":"AAAA"}},
					{"inlineData":{"mimeType":"image/jpeg","data":"BBBB"}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("gpt-4o", input, false)
	content := gjson.GetBytes(out, "messages.0.content")
	if !content.IsArray() {
		t.Fatalf("expected multimodal content array, got: %s", string(out))
	}
	if got := content.Get("1.image_url.url").String(); got != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected snake_case inline image URL %q: %s", got, content.Raw)
	}
	if got := content.Get("2.image_url.url").String(); got != "data:image/jpeg;base64,BBBB" {
		t.Fatalf("unexpected camelCase inline image URL %q: %s", got, content.Raw)
	}
}
