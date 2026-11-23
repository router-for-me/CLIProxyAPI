package chat_completions

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertOpenAIRequestToAntigravityNormalizesFileDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`)

	out, _ := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	inlineData := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.inlineData")
	if got := inlineData.Get("mimeType").String(); got != "application/pdf" {
		t.Fatalf("inlineData.mimeType = %q, want application/pdf. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}

func TestConvertOpenAIRequestToAntigravityNormalizesAssistantImageDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;charset=utf-8;base64,iVBORw0KGgo="}}]}]}`)

	out, err := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	if err != nil {
		t.Fatalf("ConvertOpenAIRequestToAntigravity error: %v", err)
	}
	inlineData := ojson.GetBytesNoCopy(out, "request.contents.0.parts.0.inlineData")
	if got := inlineData.Get("mimeType").String(); got != "image/png" {
		t.Fatalf("inlineData.mimeType = %q, want image/png. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "iVBORw0KGgo=" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}
