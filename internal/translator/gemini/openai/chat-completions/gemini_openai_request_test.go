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
	if got := part.Get("thoughtSignature").String(); got != geminiFunctionThoughtSignature {
		t.Fatalf("thoughtSignature = %q, want bypass sentinel. Output: %s", got, result)
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

func TestConvertOpenAIRequestToGemini_ResponseFormatJSONSchema(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3.1-flash-lite",
		"temperature": 0.2,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "text",
						"text": "Return structured JSON."
					}
				]
			}
		],
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "response",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"cleanedContent": {
							"type": "string"
						}
					},
					"required": [
						"cleanedContent"
					],
					"additionalProperties": false
				}
			}
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	schema := genConfig.Get("responseJsonSchema")
	if !schema.Exists() {
		t.Fatalf("responseJsonSchema missing. Output: %s", output)
	}
	if genConfig.Get("responseSchema").Exists() {
		t.Fatalf("responseSchema should not be set with responseJsonSchema. Output: %s", output)
	}
	if got := schema.Get("type").String(); got != "object" {
		t.Fatalf("schema type = %q, want object. Output: %s", got, output)
	}
	if got := schema.Get("properties.cleanedContent.type").String(); got != "string" {
		t.Fatalf("cleanedContent type = %q, want string. Output: %s", got, output)
	}
	if additionalProperties := schema.Get("additionalProperties"); !additionalProperties.Exists() || additionalProperties.Bool() {
		t.Fatalf("additionalProperties = %s, want false. Output: %s", additionalProperties.Raw, output)
	}
	if got := genConfig.Get("temperature").Float(); got != 0.2 {
		t.Fatalf("temperature = %v, want 0.2. Output: %s", got, output)
	}
}

func TestConvertOpenAIRequestToGemini_ResponseFormatJSONObject(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"messages": [{"role": "user", "content": "Return a JSON object."}],
		"response_format": {
			"type": "json_object"
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if genConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set for json_object. Output: %s", output)
	}
}

func TestConvertOpenAIRequestToGemini_ResponseFormatAbsent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"messages": [{"role": "user", "content": "plain text, no response_format"}],
		"temperature": 0.5
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)

	if result.Get("generationConfig.responseMimeType").Exists() {
		t.Fatalf("responseMimeType should not be set when response_format absent. Output: %s", output)
	}
	if result.Get("generationConfig.responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set when response_format absent. Output: %s", output)
	}
	if got := result.Get("generationConfig.temperature").Float(); got != 0.5 {
		t.Fatalf("temperature = %v, want 0.5. Output: %s", got, output)
	}
}

func TestConvertOpenAIRequestToGemini_ResponseFormatJSONSchemaNoSchema(t *testing.T) {
	// json_schema without an inner schema field: responseMimeType is still set,
	// but responseJsonSchema is not (there is nothing to embed).
	inputJSON := `{
		"model": "gemini-flash-lite",
		"messages": [{"role": "user", "content": "shape only"}],
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "response"
			}
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if genConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set when no schema provided. Output: %s", output)
	}
}

func TestConvertOpenAIRequestToGemini_ResponseFormatUnknownType(t *testing.T) {
	// An unrecognized response_format.type is a no-op: no responseMimeType,
	// no responseJsonSchema, no spurious generationConfig created. Sibling
	// fields like temperature still pass through.
	inputJSON := `{
		"model": "gemini-flash-lite",
		"messages": [{"role": "user", "content": "plain text"}],
		"temperature": 0.7,
		"response_format": {
			"type": "text"
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)

	if result.Get("generationConfig.responseMimeType").Exists() {
		t.Fatalf("responseMimeType should not be set for unknown type. Output: %s", output)
	}
	if result.Get("generationConfig.responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set for unknown type. Output: %s", output)
	}
	if got := result.Get("generationConfig.temperature").Float(); got != 0.7 {
		t.Fatalf("temperature = %v, want 0.7. Output: %s", got, output)
	}
}

func TestConvertOpenAIRequestToGemini_ResponseFormatPreservesUserGenerationConfig(t *testing.T) {
	// A user-supplied generationConfig must be preserved; response_format only
	// adds responseMimeType/responseJsonSchema on top rather than replacing it.
	inputJSON := `{
		"model": "gemini-flash-lite",
		"messages": [{"role": "user", "content": "merge check"}],
		"generationConfig": {
			"temperature": 0.9,
			"topP": 0.8
		},
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"schema": {
					"type": "object",
					"properties": {"answer": {"type": "string"}},
					"required": ["answer"],
					"additionalProperties": false
				}
			}
		}
	}`

	output := ConvertOpenAIRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	schema := genConfig.Get("responseJsonSchema")
	if !schema.Exists() {
		t.Fatalf("responseJsonSchema missing. Output: %s", output)
	}
	if got := schema.Get("type").String(); got != "object" {
		t.Fatalf("schema type = %q, want object. Output: %s", got, output)
	}
	// User-supplied generationConfig fields survive the merge.
	if got := genConfig.Get("temperature").Float(); got != 0.9 {
		t.Fatalf("user temperature = %v, want 0.9. Output: %s", got, output)
	}
	if got := genConfig.Get("topP").Float(); got != 0.8 {
		t.Fatalf("user topP = %v, want 0.8. Output: %s", got, output)
	}
}
