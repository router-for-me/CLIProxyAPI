package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

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
