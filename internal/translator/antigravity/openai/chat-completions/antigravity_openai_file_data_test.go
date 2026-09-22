package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravityNormalizesFileDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	inlineData := gjson.GetBytes(out, "request.contents.0.parts.0.inlineData")
	if got := inlineData.Get("mimeType").String(); got != "application/pdf" {
		t.Fatalf("inlineData.mimeType = %q, want application/pdf. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("inlineData.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}
