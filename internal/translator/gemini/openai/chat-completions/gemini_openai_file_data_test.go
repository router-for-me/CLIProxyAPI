package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiNormalizesFileDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`)

	out := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	inlineData := gjson.GetBytes(out, "contents.0.parts.0.inlineData")
	if got := inlineData.Get("mime_type").String(); got != "application/pdf" {
		t.Fatalf("inlineData.mime_type = %q, want application/pdf. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}
