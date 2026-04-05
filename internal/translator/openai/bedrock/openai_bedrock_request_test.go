package bedrock

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestParseDataURIImage(t *testing.T) {
	format, data, ok := parseDataURIImage("data:image/png;base64,QUJD")
	if !ok {
		t.Fatal("expected valid data URI image")
	}
	if format != "png" {
		t.Fatalf("format = %q, want %q", format, "png")
	}
	if data != "QUJD" {
		t.Fatalf("data = %q, want %q", data, "QUJD")
	}
}

func TestParseDataURIImage_InvalidMediaType(t *testing.T) {
	if _, _, ok := parseDataURIImage("data:invalid;base64,QUJD"); ok {
		t.Fatal("expected invalid media type to be rejected")
	}
}

func TestConvertOpenAIRequestToBedrock_IgnoresInvalidImageDataURI(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"user",
				"content":[
					{"type":"text","text":"hello"},
					{"type":"image_url","image_url":{"url":"data:invalid;base64,QUJD"}}
				]
			}
		]
	}`)

	out := ConvertOpenAIRequestToBedrock("deepseek.r1-v1:0", input, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid json: %s", string(out))
	}
	if gjson.GetBytes(out, "messages.0.content.1.image").Exists() {
		t.Fatalf("invalid image data URI should not emit image block: %s", string(out))
	}
}
