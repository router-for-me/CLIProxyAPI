package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToInteractionsNormalizesFileDataURL(t *testing.T) {
	input := []byte(`{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[{"type":"file","file":{"filename":"test.pdf","file_data":"data:application/pdf;base64,JVBERi0xLjQK"}}]}]}`)

	out := ConvertOpenAIRequestToInteractions("gemini-3.5-flash", input, false)
	document := gjson.GetBytes(out, "input.0.content.0")
	if got := document.Get("mime_type").String(); got != "application/pdf" {
		t.Fatalf("document.mime_type = %q, want application/pdf. Output: %s", got, out)
	}
	if got := document.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("document.data = %q, want raw base64 payload. Output: %s", got, out)
	}
}

func TestConvertOpenAIRequestToInteractionsPreservesRawFileDataWithMIMEType(t *testing.T) {
	input := []byte(`{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[{"type":"document","mime_type":"application/pdf","data":"JVBERi0xLjQK"}]}]}`)

	out := ConvertOpenAIRequestToInteractions("gemini-3.5-flash", input, false)
	document := gjson.GetBytes(out, "input.0.content.0")
	if got := document.Get("mime_type").String(); got != "application/pdf" {
		t.Fatalf("document.mime_type = %q, want application/pdf. Output: %s", got, out)
	}
	if got := document.Get("data").String(); got != "JVBERi0xLjQK" {
		t.Fatalf("document.data = %q, want unchanged raw base64 payload. Output: %s", got, out)
	}
}
