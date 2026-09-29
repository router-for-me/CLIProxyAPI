package chat_completions

import (
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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

func TestConvertOpenAIRequestToGeminiPreservesReasoningContent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "reasoning_content": "thinking only"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents length = %d, want 3. Output: %s", len(contents), result)
	}
	part := contents[1].Get("parts.0")
	if got := contents[1].Get("role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model. Output: %s", got, result)
	}
	if got := part.Get("text").String(); got != "thinking only" {
		t.Fatalf("reasoning text = %q, want thinking only. Output: %s", got, result)
	}
	if !part.Get("thought").Bool() {
		t.Fatalf("reasoning part should be marked as thought. Output: %s", result)
	}
	if part.Get("thoughtSignature").Exists() {
		t.Fatalf("reasoning part should not synthesize thoughtSignature; output=%s", result)
	}
}

func TestConvertOpenAIRequestToGeminiPreservesReasoningBeforeVisibleContentAndToolCall(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "visible answer", "reasoning_content": "thinking only", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	parts := contents[1].Get("parts").Array()
	if len(parts) != 3 {
		t.Fatalf("model parts length = %d, want 3. Output: %s", len(parts), result)
	}
	if got := parts[0].Get("text").String(); got != "thinking only" || !parts[0].Get("thought").Bool() {
		t.Fatalf("first part should be the reasoning thought. Output: %s", result)
	}
	if parts[0].Get("thoughtSignature").Exists() {
		t.Fatalf("first part should not synthesize thoughtSignature. Output: %s", result)
	}
	if got := parts[1].Get("text").String(); got != "visible answer" || parts[1].Get("thought").Bool() {
		t.Fatalf("second part should be visible assistant content. Output: %s", result)
	}
	if got := parts[2].Get("functionCall.name").String(); got != "read_file" {
		t.Fatalf("functionCall.name = %q, want read_file. Output: %s", got, result)
	}
	if got := parts[2].Get("thoughtSignature").String(); got != geminiFunctionThoughtSignature {
		t.Fatalf("functionCall thoughtSignature = %q, want bypass sentinel. Output: %s", got, result)
	}
	if got := contents[2].Get("parts.0.functionResponse.name").String(); got != "read_file" {
		t.Fatalf("functionResponse.name = %q, want read_file. Output: %s", got, result)
	}
}

func TestConvertOpenAIRequestToGeminiSkipsEmptyAssistantMessages(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "tool_calls": [{"type": "function", "function": {"name": "", "arguments": "{}"}}, {"type": "custom"}]},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2. Output: %s", len(contents), result)
	}
}

func TestConvertOpenAIRequestToGemini_MidSessionDeveloperMessageDoesNotMutateSystemInstruction(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "developer", "content": "<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>"},
			{"role": "user", "content": "Turn 2 user"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	// systemInstruction must contain only original system prompt
	sysParts := output.Get("systemInstruction.parts").Array()
	if len(sysParts) != 1 {
		t.Fatalf("systemInstruction parts = %d, want 1. Output: %s", len(sysParts), result)
	}
	if got := sysParts[0].Get("text").String(); got != "You are a helpful assistant" {
		t.Fatalf("systemInstruction text = %q, want %q", got, "You are a helpful assistant")
	}

	// contents must contain user, model, user (demoted dev message), user
	contents := output.Get("contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	if contents[0].Get("role").String() != "user" || contents[0].Get("parts.0.text").String() != "Turn 1 user" {
		t.Fatalf("turn 0 mismatch: %s", contents[0].Raw)
	}
	if contents[1].Get("role").String() != "model" || contents[1].Get("parts.0.text").String() != "Turn 1 assistant" {
		t.Fatalf("turn 1 mismatch: %s", contents[1].Raw)
	}
	expectedDevText := "<system-reminder>\n<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>\n</system-reminder>"
	if contents[2].Get("role").String() != "user" || contents[2].Get("parts.0.text").String() != expectedDevText {
		t.Fatalf("turn 2 mismatch: %s", contents[2].Raw)
	}
	if contents[3].Get("role").String() != "user" || contents[3].Get("parts.0.text").String() != "Turn 2 user" {
		t.Fatalf("turn 3 mismatch: %s", contents[3].Raw)
	}
}

func TestConvertOpenAIRequestToGemini_MidSessionSystemReminderEnvelope(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"},
			{"role": "system", "content": "Please decide which tool to call next."},
			{"role": "user", "content": "Search for news"}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	contents := output.Get("contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	expectedReminder := "<system-reminder>\nPlease decide which tool to call next.\n</system-reminder>"
	if got := contents[2].Get("parts.0.text").String(); got != expectedReminder {
		t.Fatalf("mid-session system reminder mismatch:\ngot:  %q\nwant: %q", got, expectedReminder)
	}
}

func TestConvertOpenAIRequestToGemini_MidSessionTransientSystemInstructionPreservesTurnBoundaries(t *testing.T) {
	turnWithTransient := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "System prompt"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "system", "content": "Call tool now"},
			{"role": "user", "content": "Turn 2 user"}
		]
	}`

	turnWithoutTransient := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "System prompt"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "user", "content": "Turn 2 user"},
			{"role": "assistant", "content": "Turn 2 assistant"}
		]
	}`

	outWith := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(turnWithTransient), false)
	outWithout := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(turnWithoutTransient), false)

	contentsWith := gjson.GetBytes(outWith, "contents").Array()
	contentsWithout := gjson.GetBytes(outWithout, "contents").Array()

	// Ensure demoted system instruction is standalone and not merged into adjacent user turn
	if len(contentsWith) != 4 {
		t.Fatalf("expected 4 standalone content items in request with transient instruction, got %d", len(contentsWith))
	}
	expectedReminder := "<system-reminder>\nCall tool now\n</system-reminder>"
	if contentsWith[2].Get("role").String() != "user" || contentsWith[2].Get("parts.0.text").String() != expectedReminder {
		t.Fatalf("turn 2 mismatch: %s", contentsWith[2].Raw)
	}
	if contentsWith[3].Get("role").String() != "user" || contentsWith[3].Get("parts.0.text").String() != "Turn 2 user" {
		t.Fatalf("turn 3 mismatch: %s", contentsWith[3].Raw)
	}

	// Prior turn history entries (Turn 1 user, Turn 1 assistant) are byte-identical
	if contentsWith[0].Raw != contentsWithout[0].Raw {
		t.Fatalf("turn 0 diverged: %s vs %s", contentsWith[0].Raw, contentsWithout[0].Raw)
	}
	if contentsWith[1].Raw != contentsWithout[1].Raw {
		t.Fatalf("turn 1 diverged: %s vs %s", contentsWith[1].Raw, contentsWithout[1].Raw)
	}
	// Turn 2 user text is also identical between turns because it was not merged
	if contentsWith[3].Get("parts.0.text").String() != contentsWithout[2].Get("parts.0.text").String() {
		t.Fatalf("turn 2 user text diverged due to merging: %s vs %s", contentsWith[3].Raw, contentsWithout[2].Raw)
	}
}

func TestConvertOpenAIRequestToGemini_MidSessionSystemReminderObjectAndArrayContent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi"},
			{"role": "system", "content": {"type": "text", "text": "Object instruction"}},
			{"role": "developer", "content": [{"type": "text", "text": "Array instruction"}]}
		]
	}`

	result := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	contents := output.Get("contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	expectedObject := "<system-reminder>\nObject instruction\n</system-reminder>"
	if got := contents[2].Get("parts.0.text").String(); got != expectedObject {
		t.Fatalf("object instruction mismatch:\ngot:  %q\nwant: %q", got, expectedObject)
	}
	expectedArray := "<system-reminder>\nArray instruction\n</system-reminder>"
	if got := contents[3].Get("parts.0.text").String(); got != expectedArray {
		t.Fatalf("array instruction mismatch:\ngot:  %q\nwant: %q", got, expectedArray)
	}
}

func TestConvertOpenAIRequestToGeminiMapsMaxTokens(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int64
	}{
		{
			name: "max_tokens",
			body: `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":30}`,
			want: 30,
		},
		{
			name: "max_completion_tokens",
			body: `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":40}`,
			want: 40,
		},
		{
			name: "max_tokens preferred over max_completion_tokens",
			body: `{"model":"gemini-2.0-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":30,"max_completion_tokens":40}`,
			want: 30,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIRequestToGemini("gemini-2.0-flash", []byte(tt.body), false)
			if got := gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int(); got != tt.want {
				t.Fatalf("generationConfig.maxOutputTokens = %d, want %d. Output: %s", got, tt.want, out)
			}
		})
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

func TestConvertOpenAIRequestToGeminiResponseFormatJSONSchema(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.1-flash-lite",
		"generationConfig": {
			"temperature": 0.2,
			"responseSchema": {"type": "string"}
		},
		"messages": [{"role": "user", "content": "Return structured JSON."}],
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "response",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {"cleanedContent": {"type": "string"}},
					"required": ["cleanedContent"],
					"additionalProperties": false
				}
			}
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	generationConfig := gjson.GetBytes(output, "generationConfig")

	if got := generationConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	schema := generationConfig.Get("responseJsonSchema")
	if !schema.Exists() {
		t.Fatalf("responseJsonSchema missing. Output: %s", output)
	}
	if generationConfig.Get("responseSchema").Exists() {
		t.Fatalf("responseSchema should be removed. Output: %s", output)
	}
	if additionalProperties := schema.Get("additionalProperties"); !additionalProperties.Exists() || additionalProperties.Bool() {
		t.Fatalf("additionalProperties = %s, want false. Output: %s", additionalProperties.Raw, output)
	}
	if got := generationConfig.Get("temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2. Output: %s", got, output)
	}
}

func TestConvertOpenAIRequestToGeminiResponseFormatJSONObject(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.1-flash-lite",
		"generationConfig": {"temperature": 0.6},
		"messages": [{"role": "user", "content": "Return a JSON object."}],
		"response_format": {"type": "json_object"}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	generationConfig := gjson.GetBytes(output, "generationConfig")

	if got := generationConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if generationConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set for json_object. Output: %s", output)
	}
	if got := generationConfig.Get("temperature").Float(); got != 0.6 {
		t.Fatalf("temperature = %v, want 0.6. Output: %s", got, output)
	}
}

func TestConvertOpenAIRequestToGeminiResponseFormatJSONSchemaWithoutSchema(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.1-flash-lite",
		"messages": [{"role": "user", "content": "Return structured JSON."}],
		"response_format": {"type": "json_schema", "json_schema": {"name": "response"}}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	generationConfig := gjson.GetBytes(output, "generationConfig")

	if got := generationConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if generationConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set without a schema. Output: %s", output)
	}
}

func TestConvertOpenAIRequestToGeminiResponseFormatNoOp(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "absent",
			body: `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"plain text"}],"temperature":0.5}`,
		},
		{
			name: "unknown type",
			body: `{"model":"gemini-3.1-flash-lite","messages":[{"role":"user","content":"plain text"}],"temperature":0.5,"response_format":{"type":"text"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(tt.body), false)
			generationConfig := gjson.GetBytes(output, "generationConfig")
			if generationConfig.Get("responseMimeType").Exists() {
				t.Fatalf("responseMimeType should not be set. Output: %s", output)
			}
			if generationConfig.Get("responseJsonSchema").Exists() {
				t.Fatalf("responseJsonSchema should not be set. Output: %s", output)
			}
			if got := generationConfig.Get("temperature").Float(); got != 0.5 {
				t.Fatalf("temperature = %v, want 0.5. Output: %s", got, output)
			}
		})
	}
}

func TestConvertOpenAIRequestToGemini_MultiTurnRepeatedToolCallID_Issue5933(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "list files"},
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "glob", "arguments": "{\"pattern\":\"*.go\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "[\"main.go\"]"},
			{"role": "user", "content": "read main.go"},
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read", "arguments": "{\"path\":\"main.go\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "package main"}
		]
	}`

	out := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)

	// In Turn 1 (contents[1] = model functionCall, contents[2] = user functionResponse):
	// functionCall.name must be "glob", and functionResponse.name must be "glob".
	call1Name := gjson.GetBytes(out, "contents.1.parts.0.functionCall.name").String()
	resp1Name := gjson.GetBytes(out, "contents.2.parts.0.functionResponse.name").String()
	resp1Result := gjson.GetBytes(out, "contents.2.parts.0.functionResponse.response.result").String()

	if call1Name != "glob" {
		t.Fatalf("turn 1 functionCall.name = %q, want glob", call1Name)
	}
	if resp1Name != "glob" {
		t.Fatalf("turn 1 functionResponse.name = %q, want glob (got overwritten by subsequent turn)", resp1Name)
	}
	if resp1Result != `"[\"main.go\"]"` {
		t.Fatalf("turn 1 functionResponse result = %q, want %q", resp1Result, `"[\"main.go\"]"`)
	}

	// In Turn 2 (contents[4] = model functionCall, contents[5] = user functionResponse):
	// functionCall.name must be "read", and functionResponse.name must be "read".
	call2Name := gjson.GetBytes(out, "contents.4.parts.0.functionCall.name").String()
	resp2Name := gjson.GetBytes(out, "contents.5.parts.0.functionResponse.name").String()
	resp2Result := gjson.GetBytes(out, "contents.5.parts.0.functionResponse.response.result").String()

	if call2Name != "read" {
		t.Fatalf("turn 2 functionCall.name = %q, want read", call2Name)
	}
	if resp2Name != "read" {
		t.Fatalf("turn 2 functionResponse.name = %q, want read", resp2Name)
	}
	if resp2Result != `"package main"` {
		t.Fatalf("turn 2 functionResponse result = %q, want %q", resp2Result, `"package main"`)
	}

	// Verify pairing validator passes without error
	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Gemini output: %v; output=%s", errPairing, out)
	}
}

func TestConvertOpenAIRequestToGemini_ParallelAndOutOfOrderToolResponses(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "run parallel tools"},
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "tool_a", "arguments": "{}"}},
					{"id": "call_2", "type": "function", "function": {"name": "tool_b", "arguments": "{}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_2", "content": "res_b"},
			{"role": "tool", "tool_call_id": "call_1", "content": "res_a"}
		]
	}`

	out := ConvertOpenAIRequestToGemini("gemini-3-flash", []byte(inputJSON), false)

	resp0Name := gjson.GetBytes(out, "contents.2.parts.0.functionResponse.name").String()
	resp0Result := gjson.GetBytes(out, "contents.2.parts.0.functionResponse.response.result").String()
	resp1Name := gjson.GetBytes(out, "contents.2.parts.1.functionResponse.name").String()
	resp1Result := gjson.GetBytes(out, "contents.2.parts.1.functionResponse.response.result").String()

	if resp0Name != "tool_a" || resp0Result != `"res_a"` {
		t.Fatalf("part 0 want tool_a / \"res_a\", got %s / %s", resp0Name, resp0Result)
	}
	if resp1Name != "tool_b" || resp1Result != `"res_b"` {
		t.Fatalf("part 1 want tool_b / \"res_b\", got %s / %s", resp1Name, resp1Result)
	}

	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPairing, out)
	}
}

func TestConvertOpenAIRequestToGemini_ToolChoice(t *testing.T) {
	t.Run("named function maps to toolConfig mode ANY and allowedFunctionNames", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "Call tool_a."}],
			"tool_choice": {"type": "function", "function": {"name": "tool_a"}},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		allowed := gjson.GetBytes(result, "toolConfig.functionCallingConfig.allowedFunctionNames").Array()
		if mode != "ANY" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'ANY', got %q. Output: %s", mode, result)
		}
		if len(allowed) != 1 || allowed[0].String() != "tool_a" {
			t.Fatalf("expected allowedFunctionNames = ['tool_a'], got %v. Output: %s", allowed, result)
		}
	})

	t.Run("none maps to mode NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "none",
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'NONE', got %q. Output: %s", mode, result)
		}
	})

	t.Run("auto maps to mode AUTO", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "auto",
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "AUTO" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'AUTO', got %q. Output: %s", mode, result)
		}
	})

	t.Run("required maps to mode ANY", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "required",
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "ANY" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'ANY', got %q. Output: %s", mode, result)
		}
	})

	t.Run("parallel_tool_calls false fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "auto",
			"parallel_tool_calls": false,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'NONE', got %q. Output: %s", mode, result)
		}
	})

	t.Run("required tool_choice with parallel_tool_calls false fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "required",
			"parallel_tool_calls": false,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'NONE', got %q. Output: %s", mode, result)
		}
	})

	t.Run("parallel_tool_calls null does not fail closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "auto",
			"parallel_tool_calls": null,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "AUTO" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'AUTO', got %q. Output: %s", mode, result)
		}
	})

	t.Run("parallel_tool_calls true does not fail closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "auto",
			"parallel_tool_calls": true,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "AUTO" {
			t.Fatalf("expected toolConfig.functionCallingConfig.mode = 'AUTO', got %q. Output: %s", mode, result)
		}
	})

	t.Run("allowed_tools filters function declarations and sets AUTO mode without allowedFunctionNames", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "auto",
					"tools": [{"type": "function", "function": {"name": "tool_b"}}]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_b", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		allowed := gjson.GetBytes(result, "toolConfig.functionCallingConfig.allowedFunctionNames")
		if mode != "AUTO" {
			t.Fatalf("expected mode = 'AUTO', got %q. Output: %s", mode, result)
		}
		if allowed.Exists() {
			t.Fatalf("expected allowedFunctionNames to not be set for AUTO mode, got %v", allowed.Value())
		}
		decls := gjson.GetBytes(result, "tools.0.functionDeclarations").Array()
		if len(decls) != 1 || decls[0].Get("name").String() != "tool_b" {
			t.Fatalf("expected functionDeclarations to contain only tool_b, got %v", decls)
		}
	})

	t.Run("allowed_tools with required mode sets mode ANY and allowedFunctionNames", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "required",
					"tools": [{"type": "function", "function": {"name": "tool_b"}}]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "tool_b", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		allowed := gjson.GetBytes(result, "toolConfig.functionCallingConfig.allowedFunctionNames").Array()
		if mode != "ANY" {
			t.Fatalf("expected mode = 'ANY', got %q. Output: %s", mode, result)
		}
		if len(allowed) != 1 || allowed[0].String() != "tool_b" {
			t.Fatalf("expected allowedFunctionNames = ['tool_b'], got %v", allowed)
		}
	})

	t.Run("empty allowed_tools fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"tools": []
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected mode = 'NONE', got %q. Output: %s", mode, result)
		}
	})

	t.Run("function choice with missing name fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {"type": "function", "function": {}},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected mode = 'NONE', got %q. Output: %s", mode, result)
		}
	})

	t.Run("allowed_tools matches exact original name and does not conflate sanitization", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"tools": [{"type": "function", "function": {"name": "1tool"}}]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "_1tool", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected mode = 'NONE' when exact original name not found, got %q. Output: %s", mode, result)
		}
	})

	t.Run("sanitized name collision fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": "auto",
			"tools": [
				{"type": "function", "function": {"name": "1tool", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "_1tool", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected mode = 'NONE' on name collision, got %q. Output: %s", mode, result)
		}
	})

	t.Run("undeclared function choice fails closed to NONE", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {"type": "function", "function": {"name": "undeclared_tool"}},
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "NONE" {
			t.Fatalf("expected mode = 'NONE' for undeclared function, got %q. Output: %s", mode, result)
		}
	})

	t.Run("allowed_tools filtering avoids false collision when excluded tool collides", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": {
				"type": "allowed_tools",
				"allowed_tools": {
					"mode": "auto",
					"tools": [{"type": "function", "function": {"name": "_1tool"}}]
				}
			},
			"tools": [
				{"type": "function", "function": {"name": "1tool", "parameters": {"type": "object", "properties": {}}}},
				{"type": "function", "function": {"name": "_1tool", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "AUTO" {
			t.Fatalf("expected mode = 'AUTO', got %q. Output: %s", mode, result)
		}
		decls := gjson.GetBytes(result, "tools.0.functionDeclarations").Array()
		if len(decls) != 1 || decls[0].Get("name").String() != "_1tool" {
			t.Fatalf("expected only _1tool to remain in functionDeclarations, got: %v", decls)
		}
	})

	t.Run("tool_choice null does not create toolConfig or fail closed", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "test"}],
			"tool_choice": null,
			"tools": [
				{"type": "function", "function": {"name": "tool_a", "parameters": {"type": "object", "properties": {}}}}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		if gjson.GetBytes(result, "toolConfig").Exists() {
			t.Fatalf("expected toolConfig not to be set when tool_choice is null, got: %s", result)
		}
	})
}

func TestConvertOpenAIRequestToGemini_ToolStrictMapsToValidatedMode(t *testing.T) {
	tests := []struct {
		name         string
		toolChoice   string // empty means absent
		expectedMode string
		allowedNames []string
	}{
		{
			name:         "absent tool_choice maps to VALIDATED",
			toolChoice:   "",
			expectedMode: "VALIDATED",
		},
		{
			name:         "explicit auto tool_choice maps to VALIDATED",
			toolChoice:   `"tool_choice": "auto",`,
			expectedMode: "VALIDATED",
		},
		{
			name:         "null tool_choice maps to VALIDATED",
			toolChoice:   `"tool_choice": null,`,
			expectedMode: "VALIDATED",
		},
		{
			name:         "required tool_choice maps to ANY",
			toolChoice:   `"tool_choice": "required",`,
			expectedMode: "ANY",
		},
		{
			name:         "none tool_choice maps to NONE",
			toolChoice:   `"tool_choice": "none",`,
			expectedMode: "NONE",
		},
		{
			name:         "specific function tool_choice maps to ANY with allowedFunctionNames",
			toolChoice:   `"tool_choice": {"type": "function", "function": {"name": "tool_a"}},`,
			expectedMode: "ANY",
			allowedNames: []string{"tool_a"},
		},
		{
			name:         "allowed_tools with auto mode and strict tool maps to VALIDATED",
			toolChoice:   `"tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "auto", "tools": [{"type": "function", "function": {"name": "tool_a"}}]}},`,
			expectedMode: "VALIDATED",
		},
		{
			name:         "allowed_tools with required mode and strict tool maps to ANY",
			toolChoice:   `"tool_choice": {"type": "allowed_tools", "allowed_tools": {"mode": "required", "tools": [{"type": "function", "function": {"name": "tool_a"}}]}},`,
			expectedMode: "ANY",
			allowedNames: []string{"tool_a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := fmt.Sprintf(`{
				"model": "gemini-3.1-pro-high",
				"messages": [{"role": "user", "content": "hi"}],
				%s
				"tools": [
					{
						"type": "function",
						"function": {
							"name": "tool_a",
							"description": "Controlled tool.",
							"strict": true,
							"parameters": {"type": "object", "properties": {}}
						}
					}
				]
			}`, tt.toolChoice)
			result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
			if gjson.GetBytes(result, "tools.0.functionDeclarations.0.strict").Exists() {
				t.Fatalf("tools.0.functionDeclarations.0.strict should be removed from function declaration: %s", result)
			}
			mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
			if mode != tt.expectedMode {
				t.Fatalf("expected toolConfig.functionCallingConfig.mode = %q, got %q. Output: %s", tt.expectedMode, mode, result)
			}
			if len(tt.allowedNames) > 0 {
				allowed := gjson.GetBytes(result, "toolConfig.functionCallingConfig.allowedFunctionNames").Array()
				if len(allowed) != len(tt.allowedNames) {
					t.Fatalf("expected %d allowedFunctionNames, got %d", len(tt.allowedNames), len(allowed))
				}
				for i, name := range tt.allowedNames {
					if allowed[i].String() != name {
						t.Fatalf("allowedFunctionNames[%d] = %q, want %q", i, allowed[i].String(), name)
					}
				}
			}
		})
	}

	t.Run("mixed tools where one is strict maps to VALIDATED", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "tool_a",
						"description": "Loose tool.",
						"strict": false,
						"parameters": {"type": "object", "properties": {}}
					}
				},
				{
					"type": "function",
					"function": {
						"name": "tool_b",
						"description": "Strict tool.",
						"strict": true,
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		mode := gjson.GetBytes(result, "toolConfig.functionCallingConfig.mode").String()
		if mode != "VALIDATED" {
			t.Fatalf("expected mode = 'VALIDATED' for mixed tools, got %q", mode)
		}
	})

	t.Run("non-strict tools omit toolConfig mode", func(t *testing.T) {
		inputJSON := `{
			"model": "gemini-3.1-pro-high",
			"messages": [{"role": "user", "content": "hi"}],
			"tools": [
				{
					"type": "function",
					"function": {
						"name": "tool_a",
						"description": "Loose tool.",
						"strict": false,
						"parameters": {"type": "object", "properties": {}}
					}
				},
				{
					"type": "function",
					"function": {
						"name": "tool_b",
						"description": "Unspecified tool.",
						"parameters": {"type": "object", "properties": {}}
					}
				}
			]
		}`
		result := ConvertOpenAIRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
		if gjson.GetBytes(result, "toolConfig").Exists() {
			t.Fatalf("expected toolConfig not to be set when no strict tools and no tool_choice, got: %s", result)
		}
	})
}

func TestConvertOpenAIRequestToGemini_ParametersJsonSchema_PreservesAdditionalPropertiesAndPattern_Issue5959(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-2.5-flash",
		"messages": [{"role": "user", "content": "Use the submit tool."}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "submit",
					"description": "Submit a bounded schema test value.",
					"parameters": {
						"$schema": "https://json-schema.org/draft/2020-12/schema",
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"recipient": {
								"type": "string",
								"pattern": "^(alice|bob)$"
							},
							"amount": {
								"type": "number"
							}
						},
						"required": ["recipient", "amount"]
					}
				}
			}
		]
	}`)

	output := ConvertOpenAIRequestToGemini("gemini-2.5-flash", inputJSON, false)
	schema := gjson.GetBytes(output, "tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("parametersJsonSchema missing. Output: %s", output)
	}
	if got := schema.Get("additionalProperties"); !got.Exists() || got.Type != gjson.False {
		t.Fatalf("additionalProperties should be preserved as false, got: %v. Schema: %s", got, schema.Raw)
	}
	if got := schema.Get("properties.recipient.pattern"); !got.Exists() || got.String() != "^(alice|bob)$" {
		t.Fatalf("pattern should be preserved, got: %v. Schema: %s", got, schema.Raw)
	}
	if schema.Get("description").Exists() && schema.Get("description").String() == "No extra properties allowed" {
		t.Fatalf("additionalProperties: false should not be converted to description hint. Schema: %s", schema.Raw)
	}
	if got := schema.Get("properties.recipient.description"); got.Exists() && strings.Contains(got.String(), "pattern:") {
		t.Fatalf("pattern should not be converted to description hint. Schema: %s", schema.Raw)
	}
}
