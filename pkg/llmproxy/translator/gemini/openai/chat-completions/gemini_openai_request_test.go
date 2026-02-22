package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGeminiRemovesUnsupportedGoogleSearchFields(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"google_search":{"defer_loading":true,"deferLoading":true,"lat":"1"}}
		]
	}`)

	got := ConvertOpenAIRequestToGemini("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	tool := res.Get("tools.0.googleSearch")
	if !tool.Exists() {
		t.Fatalf("expected googleSearch tool to exist")
	}
	if tool.Get("defer_loading").Exists() {
		t.Fatalf("expected defer_loading to be removed")
	}
	if tool.Get("deferLoading").Exists() {
		t.Fatalf("expected deferLoading to be removed")
	}
	if tool.Get("lat").String() != "1" {
		t.Fatalf("expected non-problematic fields to remain")
	}
}
