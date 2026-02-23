package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravitySkipsEmptyAssistantMessage(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":""},
			{"role":"user","content":"second"}
		]
	}`)

	got := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	if count := len(res.Get("request.contents").Array()); count != 2 {
		t.Fatalf("expected 2 request.contents entries (assistant empty skipped), got %d", count)
	}
	if res.Get("request.contents.0.role").String() != "user" || res.Get("request.contents.1.role").String() != "user" {
		t.Fatalf("expected only user entries, got %s", res.Get("request.contents").Raw)
	}
}

func TestConvertOpenAIRequestToAntigravityRemovesUnsupportedGoogleSearchFields(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{"google_search":{"defer_loading":true,"deferLoading":true,"lat":"1"}}
		]
	}`)

	got := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	tool := res.Get("request.tools.0.googleSearch")
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
