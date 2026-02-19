package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_MapsTextFormatSchemaAndToolStrict(t *testing.T) {
	input := []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Return JSON"}]}],
		"text":{
			"format":{
				"type":"json_schema",
				"name":"result",
				"schema":{
					"type":"object",
					"properties":{"ok":{"type":"boolean"}},
					"required":["ok"],
					"additionalProperties":false
				}
			}
		},
		"tools":[
			{
				"type":"function",
				"name":"save",
				"description":"save output",
				"strict":true,
				"parameters":{
					"type":"object",
					"properties":{"ok":{"type":"boolean"}},
					"required":["ok"]
				}
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToClaude("claude-sonnet", input, false)
	raw := string(output)

	if gjson.Get(raw, "output_config.format.type").String() != "json_schema" {
		t.Fatalf("expected output_config.format.type=json_schema, got %s", gjson.Get(raw, "output_config.format.type").Raw)
	}
	if gjson.Get(raw, "output_config.format.name").String() != "result" {
		t.Fatalf("expected output_config.format.name=result, got %s", gjson.Get(raw, "output_config.format.name").Raw)
	}
	if gjson.Get(raw, "output_config.format.schema.properties.ok.type").String() != "boolean" {
		t.Fatalf("expected structured schema to be mapped, got %s", gjson.Get(raw, "output_config.format.schema").Raw)
	}
	if !gjson.Get(raw, "tools.0.strict").Bool() {
		t.Fatalf("expected tools.0.strict=true, got %s", gjson.Get(raw, "tools.0").Raw)
	}
}
