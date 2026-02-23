package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertToOpenAIFormat_GeminiDefaultsToDataURL(t *testing.T) {
	t.Parallel()

	h := &OpenAIImagesAPIHandler{}
	resp := []byte(`{
		"candidates":[
			{
				"content":{
					"parts":[
						{
							"inlineData":{
								"mimeType":"image/png",
								"data":"abc123"
							}
						}
					]
				}
			}
		]
	}`)

	got := h.convertToOpenAIFormat(resp, "gemini-2.5-flash-image", "cat", "")
	if len(got.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got.Data))
	}
	if got.Data[0].URL != "data:image/png;base64,abc123" {
		t.Fatalf("expected data URL, got %q", got.Data[0].URL)
	}
	if got.Data[0].B64JSON != "" {
		t.Fatalf("expected empty b64_json for default response format, got %q", got.Data[0].B64JSON)
	}
}

func TestConvertToOpenAIFormat_GeminiB64JSONResponseFormat(t *testing.T) {
	t.Parallel()

	h := &OpenAIImagesAPIHandler{}
	resp := []byte(`{
		"candidates":[
			{
				"content":{
					"parts":[
						{
							"inlineData":{
								"mimeType":"image/png",
								"data":"base64payload"
							}
						}
					]
				}
			}
		]
	}`)

	got := h.convertToOpenAIFormat(resp, "imagen-4.0-generate-001", "mountain", "b64_json")
	if len(got.Data) != 1 {
		t.Fatalf("expected 1 image, got %d", len(got.Data))
	}
	if got.Data[0].B64JSON != "base64payload" {
		t.Fatalf("expected b64_json payload, got %q", got.Data[0].B64JSON)
	}
	if got.Data[0].URL != "" {
		t.Fatalf("expected empty URL for b64_json response, got %q", got.Data[0].URL)
	}
}

func TestConvertToProviderFormat_GeminiMapsSizeToAspectRatio(t *testing.T) {
	t.Parallel()

	h := &OpenAIImagesAPIHandler{}
	raw := []byte(`{
		"model":"gemini-2.5-flash-image",
		"prompt":"draw",
		"size":"1792x1024",
		"n":2
	}`)

	out := h.convertToProviderFormat("gemini-2.5-flash-image", raw)
	if got := gjson.GetBytes(out, "generationConfig.imageConfig.aspectRatio").String(); got != "16:9" {
		t.Fatalf("expected aspectRatio 16:9, got %q", got)
	}
	if got := gjson.GetBytes(out, "generationConfig.sampleCount").Int(); got != 2 {
		t.Fatalf("expected sampleCount 2, got %d", got)
	}
}
