package chat_completions

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertOpenAIRequestToGeminiNormalizesFileDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`)

	out, _ := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	inlineData := ojson.GetBytesNoCopy(out, "contents.0.parts.0.inlineData")
	if got := inlineData.Get("mime_type").String(); got != "application/pdf" {
		t.Fatalf("inlineData.mime_type = %q, want application/pdf. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}

func TestConvertOpenAIRequestToGeminiNormalizesAssistantImageDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"generate an image"},{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;charset=utf-8;base64,iVBORw0KGgo="}}]},{"role":"user","content":"describe it"}]}`)

	out, errConvert := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	if errConvert != nil {
		t.Fatalf("ConvertOpenAIRequestToGemini error: %v", errConvert)
	}
	inlineData := ojson.GetBytesNoCopy(out, "contents.1.parts.0.inlineData")
	if got := inlineData.Get("mime_type").String(); got != "image/png" {
		t.Fatalf("inlineData.mime_type = %q, want image/png. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "iVBORw0KGgo=" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}
