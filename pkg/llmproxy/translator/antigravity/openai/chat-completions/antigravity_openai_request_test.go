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
