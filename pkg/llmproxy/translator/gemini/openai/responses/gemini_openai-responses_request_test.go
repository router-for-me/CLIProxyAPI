package responses

import (
<<<<<<< HEAD:pkg/llmproxy/translator/gemini/openai/responses/gemini_openai-responses_request_test.go
	"strings"
=======
	"encoding/base64"
>>>>>>> upstream/main:internal/translator/gemini/openai/responses/gemini_openai-responses_request_test.go
	"testing"

	"github.com/tidwall/gjson"
)

<<<<<<< HEAD:pkg/llmproxy/translator/gemini/openai/responses/gemini_openai-responses_request_test.go
func TestConvertOpenAIResponsesRequestToGeminiFunctionCall(t *testing.T) {
	input := []byte(`{
		"model": "gemini-2.0-flash",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"What's the forecast?"}]},
			{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{\"city\":\"SF\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"{\"temp\":72}"}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)

	first := res.Get("contents.0")
	if first.Get("role").String() != "user" {
		t.Fatalf("contents[0].role = %s, want user", first.Get("role").String())
	}
	if first.Get("parts.0.text").String() != "What's the forecast?" {
		t.Fatalf("unexpected first part text: %q", first.Get("parts.0.text").String())
	}

	second := res.Get("contents.1")
	if second.Get("role").String() != "model" {
		t.Fatalf("contents[1].role = %s, want model", second.Get("role").String())
	}
	if second.Get("parts.0.functionCall.name").String() != "weather" {
		t.Fatalf("unexpected function name: %s", second.Get("parts.0.functionCall.name").String())
	}

	third := res.Get("contents.2")
	if third.Get("role").String() != "function" {
		t.Fatalf("contents[2].role = %s, want function", third.Get("role").String())
	}
	if third.Get("parts.0.functionResponse.name").String() != "weather" {
		t.Fatalf("unexpected function response name: %s", third.Get("parts.0.functionResponse.name").String())
	}
}

func TestConvertOpenAIResponsesRequestToGeminiSkipsEmptyTextParts(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.0-flash",
		"input":[
			{"type":"message","role":"user","content":[
				{"type":"input_text","text":"   "},
				{"type":"input_text","text":"real prompt"}
			]}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)
	if res.Get("contents.0.parts.#").Int() != 1 {
		t.Fatalf("expected only one non-empty text part, got %s", res.Get("contents.0.parts").Raw)
	}
	if res.Get("contents.0.parts.0.text").String() != "real prompt" {
		t.Fatalf("expected surviving text part to be preserved")
	}
}

func TestConvertOpenAIResponsesRequestToGeminiMapsMaxOutputTokens(t *testing.T) {
	input := []byte(`{"model":"gemini-2.0-flash","input":"hello","max_output_tokens":123}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)
	if res.Get("generationConfig.maxOutputTokens").Int() != 123 {
		t.Fatalf("generationConfig.maxOutputTokens = %d, want 123", res.Get("generationConfig.maxOutputTokens").Int())
	}
}

func TestConvertOpenAIResponsesRequestToGeminiRemovesUnsupportedSchemaFields(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.0-flash",
		"input":"hello",
		"tools":[
			{
				"type":"function",
				"name":"search",
				"description":"search data",
				"parameters":{
					"type":"object",
					"$id":"urn:search",
					"properties":{"query":{"type":"string"}},
					"patternProperties":{"^x-":{"type":"string"}}
				}
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)
	schema := res.Get("tools.0.functionDeclarations.0.parametersJsonSchema")
	if !schema.Exists() {
		t.Fatalf("expected parametersJsonSchema to exist")
	}
	if schema.Get("$id").Exists() {
		t.Fatalf("expected $id to be removed")
	}
	if schema.Get("patternProperties").Exists() {
		t.Fatalf("expected patternProperties to be removed")
	}
}

func TestConvertOpenAIResponsesRequestToGeminiHandlesNullableTypeArrays(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.0-flash",
		"input":"hello",
		"tools":[
			{
				"type":"function",
				"name":"write_file",
				"description":"write file content",
				"parameters":{
					"type":"object",
					"properties":{
						"path":{"type":"string"},
						"content":{"type":["string","null"]}
					},
					"required":["path"]
				}
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)

	contentType := res.Get("tools.0.functionDeclarations.0.parametersJsonSchema.properties.content.type")
	if !contentType.Exists() {
		t.Fatalf("expected content.type to exist after schema normalization")
	}
	if contentType.Type == gjson.String && strings.HasPrefix(contentType.String(), "[") {
		t.Fatalf("expected content.type not to be stringified type array, got %q", contentType.String())
	}
}

func TestConvertOpenAIResponsesRequestToGeminiStrictSchemaClosesAdditionalProperties(t *testing.T) {
	input := []byte(`{
		"model":"gemini-2.0-flash",
		"input":"hello",
		"tools":[
			{
				"type":"function",
				"name":"write_file",
				"description":"write file content",
				"strict":true,
				"parameters":{
					"type":"object",
					"properties":{"path":{"type":"string"}}
				}
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", input, false)
	res := gjson.ParseBytes(got)

	if !res.Get("tools.0.functionDeclarations.0.parametersJsonSchema.additionalProperties").Exists() {
		t.Fatalf("expected strict schema to set additionalProperties")
	}
	if res.Get("tools.0.functionDeclarations.0.parametersJsonSchema.additionalProperties").Bool() {
		t.Fatalf("expected additionalProperties=false for strict schema")
	}
}
=======
const testResponsesGeminiThoughtSignature = "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"

func TestConvertOpenAIResponsesRequestToGemini_StripsTrailingAssistantPrefill(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "previous answer"}]
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-high", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	contents := resultJSON.Get("contents").Array()

	if len(contents) != 1 {
		t.Fatalf("contents length = %d, want 1. contents=%s", len(contents), resultJSON.Get("contents").Raw)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("final remaining role = %q, want %q", got, "user")
	}
}

func TestConvertOpenAIResponsesRequestToGemini_TextFormatJSONSchema(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"temperature": 0.2,
		"input": [
			{
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Return structured JSON."
					}
				]
			}
		],
		"text": {
			"format": {
				"type": "json_schema",
				"strict": true,
				"name": "response",
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

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
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

func TestConvertOpenAIResponsesRequestToGemini_TextFormatJSONObject(t *testing.T) {
	inputJSON := `{
		"model": "gemini-flash-lite",
		"input": "Return a JSON object.",
		"text": {
			"format": {
				"type": "json_object"
			}
		}
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-flash-lite", []byte(inputJSON), false)
	result := gjson.ParseBytes(output)
	genConfig := result.Get("generationConfig")

	if got := genConfig.Get("responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, output)
	}
	if genConfig.Get("responseJsonSchema").Exists() {
		t.Fatalf("responseJsonSchema should not be set for json_object. Output: %s", output)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ReasoningSignatureCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		encrypted     string
		wantSignature string
	}{
		{
			name:          "GPT encrypted_content uses Gemini bypass",
			encrypted:     validResponsesGPTReasoningSignature(),
			wantSignature: geminiResponsesThoughtSignature,
		},
		{
			name:          "Gemini encrypted_content is preserved",
			encrypted:     "gemini#" + testResponsesGeminiThoughtSignature,
			wantSignature: testResponsesGeminiThoughtSignature,
		},
		{
			name:          "Missing encrypted_content uses Gemini bypass",
			encrypted:     "",
			wantSignature: geminiResponsesThoughtSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"model": "gpt-5",
				"input": [{
					"type": "reasoning",
					"encrypted_content": "` + tt.encrypted + `",
					"summary": [{"type": "summary_text", "text": "reasoning summary"}]
				}]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			part := gjson.GetBytes(output, "contents.0.parts.0")
			if got := part.Get("thoughtSignature").String(); got != tt.wantSignature {
				t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, tt.wantSignature, output)
			}
			if got := part.Get("text").String(); got != "reasoning summary" {
				t.Fatalf("thought text = %q, want reasoning summary. Output: %s", got, output)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_SystemAndDeveloperRoles(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		wantText string
	}{
		{
			name:     "system role",
			role:     "system",
			wantText: "System message text",
		},
		{
			name:     "developer role",
			role:     "developer",
			wantText: "Developer message text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"instructions": "Be a helpful assistant",
				"input": [
					{
						"type": "message",
						"role": "` + tt.role + `",
						"content": [
							{
								"type": "input_text",
								"text": "` + tt.wantText + `"
							}
						]
					},
					{
						"type": "message",
						"role": "user",
						"content": [
							{
								"type": "input_text",
								"text": "Hello"
							}
						]
					}
				]
			}`)

			output := ConvertOpenAIResponsesRequestToGemini("gemini-3.5-flash", input, false)
			result := gjson.ParseBytes(output)

			systemInstruction := result.Get("systemInstruction")
			if !systemInstruction.Exists() {
				t.Fatalf("systemInstruction missing. Output: %s", output)
			}
			parts := systemInstruction.Get("parts")
			if got := parts.Get("#").Int(); got != 2 {
				t.Fatalf("systemInstruction parts = %d, want 2. Output: %s", got, output)
			}
			if got := parts.Get("0.text").String(); got != "Be a helpful assistant" {
				t.Fatalf("first systemInstruction part = %q, want %q. Output: %s", got, "Be a helpful assistant", output)
			}
			if got := parts.Get("1.text").String(); got != tt.wantText {
				t.Fatalf("second systemInstruction part = %q, want %q. Output: %s", got, tt.wantText, output)
			}

			result.Get("contents").ForEach(func(_, value gjson.Result) bool {
				if role := value.Get("role").String(); role == tt.role {
					t.Fatalf("role %q leaked into contents array. Output: %s", tt.role, output)
				}
				return true
			})
		})
	}
}

func TestConvertOpenAIResponsesRequestToGeminiCleansToolSchemaRequiredFields(t *testing.T) {
	inputJSON := `{
		"model": "gemini-2.0-flash",
		"input": "hi",
		"tools": [{
			"type": "function",
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
		}]
	}`

	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.0-flash", []byte(inputJSON), false)
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

func validResponsesGPTReasoningSignature() string {
	raw := make([]byte, 1+8+16+16+32)
	raw[0] = 0x80
	raw[8] = 1
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(raw)
}
>>>>>>> upstream/main:internal/translator/gemini/openai/responses/gemini_openai-responses_request_test.go
