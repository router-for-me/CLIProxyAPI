package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

<<<<<<< HEAD:pkg/llmproxy/translator/antigravity/openai/chat-completions/antigravity_openai_request_test.go
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

func TestConvertOpenAIRequestToAntigravitySkipsWhitespaceOnlyAssistantMessage(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.5-pro",
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"   \n\t  "},
			{"role":"user","content":"second"}
		]
	}`)

	got := ConvertOpenAIRequestToAntigravity("gemini-2.5-pro", input, false)
	res := gjson.ParseBytes(got)
	if count := len(res.Get("request.contents").Array()); count != 2 {
		t.Fatalf("expected 2 request.contents entries (assistant whitespace-only skipped), got %d", count)
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
=======
func TestConvertOpenAIRequestToAntigravitySkipsEmptyTextPartsWithoutNulls(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": ""},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			},
			{
				"role": "assistant",
				"content": [{"type": "text", "text": ""}],
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "done"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	userParts := gjson.GetBytes(result, "request.contents.0.parts").Array()
	if len(userParts) != 1 {
		t.Fatalf("user parts length = %d, want 1. Output: %s", len(userParts), result)
	}
	if userParts[0].Type == gjson.Null {
		t.Fatalf("user parts.0 is null. Output: %s", result)
	}
	if got := userParts[0].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want audio/mpeg. Output: %s", got, result)
	}

	assistantParts := gjson.GetBytes(result, "request.contents.1.parts").Array()
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts length = %d, want 1. Output: %s", len(assistantParts), result)
	}
	if assistantParts[0].Type == gjson.Null {
		t.Fatalf("assistant parts.0 is null. Output: %s", result)
	}
	if !assistantParts[0].Get("functionCall").Exists() {
		t.Fatalf("functionCall missing. Output: %s", result)
>>>>>>> upstream/main:internal/translator/antigravity/openai/chat-completions/antigravity_openai_request_test.go
	}
}
