package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToGemini_StripsTrailingAssistantPrefill(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "previous answer"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	contents := resultJSON.Get("contents").Array()

	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. contents=%s", len(contents), resultJSON.Get("contents").Raw)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("final remaining role = %q, want %q", got, "user")
	}
}

func TestConvertOpenAIRequestToGeminiPreservesInputAudio(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.5",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Transcribe this audio verbatim."},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	parts := resultJSON.Get("contents.0.parts").Array()

	if len(parts) != 2 {
		t.Fatalf("parts length = %d, want 2. parts=%s", len(parts), resultJSON.Get("contents.0.parts").Raw)
	}
	if got := parts[0].Get("text").String(); got != "Transcribe this audio verbatim." {
		t.Fatalf("text part = %q, want prompt text", got)
	}
	if got := parts[1].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want %q", got, "audio/mpeg")
	}
	if got := parts[1].Get("inlineData.data").String(); got != "SUQzBA==" {
		t.Fatalf("audio data = %q, want %q", got, "SUQzBA==")
	}
}

func TestConvertOpenAIRequestToGeminiPreservesVideoURL(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "video_url", "video_url": {"url": "data:video/mp4;base64,AAAAIGZ0eXBtcDQy"}},
					{"type": "text", "text": "Describe the video"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	parts := resultJSON.Get("contents.0.parts").Array()

	if len(parts) != 2 {
		t.Fatalf("parts length = %d, want 2. parts=%s", len(parts), resultJSON.Get("contents.0.parts").Raw)
	}
	if got := parts[0].Get("inlineData.mime_type").String(); got != "video/mp4" {
		t.Fatalf("video mime_type = %q, want %q", got, "video/mp4")
	}
	if got := parts[0].Get("inlineData.data").String(); got != "AAAAIGZ0eXBtcDQy" {
		t.Fatalf("video data = %q, want %q", got, "AAAAIGZ0eXBtcDQy")
	}
	if got := parts[1].Get("text").String(); got != "Describe the video" {
		t.Fatalf("text part = %q, want prompt text", got)
	}
}

func TestConvertOpenAIRequestToGeminiSkipsEmptyTextPartsWithoutNulls(t *testing.T) {
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

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)
	userParts := gjson.GetBytes(result, "contents.0.parts").Array()
	if len(userParts) != 1 {
		t.Fatalf("user parts length = %d, want 1. Output: %s", len(userParts), result)
	}
	if userParts[0].Type == gjson.Null {
		t.Fatalf("user parts.0 is null. Output: %s", result)
	}
	if got := userParts[0].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want audio/mpeg. Output: %s", got, result)
	}

	assistantParts := gjson.GetBytes(result, "contents.1.parts").Array()
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts length = %d, want 1. Output: %s", len(assistantParts), result)
	}
	if assistantParts[0].Type == gjson.Null {
		t.Fatalf("assistant parts.0 is null. Output: %s", result)
	}
	if !assistantParts[0].Get("functionCall").Exists() {
		t.Fatalf("functionCall missing. Output: %s", result)
	}
}

func TestConvertOpenAIRequestToGeminiCleansToolSchemaRequiredFields(t *testing.T) {
	inputJSON := `{
		"model": "gemini-2.0-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "search_company",
				"description": "Search",
				"parameters": {
					"type": "object",
					"title": "SearchCompany",
					"properties": {
						"country": {"type": "string"},
						"industry": {"type": "string"}
					},
					"required": ["country", "industry", "stale_field", "another_stale"]
				}
			}
		}]
	}`

	output := ConvertOpenAIRequestToGemini("gemini-2.0-flash", []byte(inputJSON), false)
	schema := gjson.GetBytes(output, "tools.0.functionDeclarations.0.parametersJsonSchema")

	if !schema.Exists() {
		t.Fatalf("parametersJsonSchema missing. Output: %s", output)
	}
	if schema.Get("title").Exists() {
		t.Fatalf("schema title should be removed. Output: %s", output)
	}
	required := schema.Get("required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2. Schema: %s", len(required), schema.Raw)
	}
	if got := required[0].String(); got != "country" {
		t.Fatalf("required[0] = %q, want country. Schema: %s", got, schema.Raw)
	}
	if got := required[1].String(); got != "industry" {
		t.Fatalf("required[1] = %q, want industry. Schema: %s", got, schema.Raw)
	}
}
