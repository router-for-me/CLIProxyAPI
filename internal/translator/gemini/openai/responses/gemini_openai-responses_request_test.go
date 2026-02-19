package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToGemini_MapsTextFormatSchema(t *testing.T) {
	input := []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Return JSON"}]}],
		"max_output_tokens":128,
		"text":{
			"format":{
				"type":"json_schema",
				"name":"result",
				"schema":{
					"type":"object",
					"properties":{"status":{"type":"string"}},
					"required":["status"],
					"additionalProperties":false
				}
			}
		}
	}`)

	output := ConvertOpenAIResponsesRequestToGemini("gemini-2.5-pro", input, false)
	raw := string(output)

	if gjson.Get(raw, "generationConfig.responseMimeType").String() != "application/json" {
		t.Fatalf("expected responseMimeType=application/json, got %s", gjson.Get(raw, "generationConfig.responseMimeType").Raw)
	}
	if !gjson.Get(raw, "generationConfig.responseJsonSchema").Exists() {
		t.Fatalf("expected responseJsonSchema to exist, got %s", gjson.Get(raw, "generationConfig").Raw)
	}
	if gjson.Get(raw, "generationConfig.responseJsonSchema.additionalProperties").Exists() {
		t.Fatalf("expected Gemini-cleaned schema without additionalProperties, got %s", gjson.Get(raw, "generationConfig.responseJsonSchema").Raw)
	}
	if gjson.Get(raw, "generationConfig.maxOutputTokens").Int() != 128 {
		t.Fatalf("expected maxOutputTokens=128, got %s", gjson.Get(raw, "generationConfig.maxOutputTokens").Raw)
	}
}
