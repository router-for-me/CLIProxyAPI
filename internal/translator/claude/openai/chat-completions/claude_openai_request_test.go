package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude_MapsStructuredOutputAndStrictTool(t *testing.T) {
	input := []byte(`{
		"messages":[{"role":"user","content":"Return JSON"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"answer_schema",
				"strict":true,
				"schema":{
					"type":"object",
					"properties":{"answer":{"type":"string"}},
					"required":["answer"],
					"additionalProperties":false
				}
			}
		},
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"save_answer",
					"description":"save",
					"strict":true,
					"parameters":{
						"type":"object",
						"properties":{"answer":{"type":"string"}},
						"required":["answer"],
						"additionalProperties":false
					}
				}
			}
		]
	}`)

	output := ConvertOpenAIRequestToClaude("claude-sonnet", input, false)
	raw := string(output)

	if gjson.Get(raw, "output_config.format.type").String() != "json_schema" {
		t.Fatalf("expected output_config.format.type=json_schema, got %s", gjson.Get(raw, "output_config.format.type").Raw)
	}
	if gjson.Get(raw, "output_config.format.name").String() != "answer_schema" {
		t.Fatalf("expected output_config.format.name=answer_schema, got %s", gjson.Get(raw, "output_config.format.name").Raw)
	}
	if gjson.Get(raw, "output_config.format.schema.properties.answer.type").String() != "string" {
		t.Fatalf("expected structured schema to be mapped, got %s", gjson.Get(raw, "output_config.format.schema").Raw)
	}
	if !gjson.Get(raw, "tools.0.strict").Bool() {
		t.Fatalf("expected strict tool mapping, got %s", gjson.Get(raw, "tools.0").Raw)
	}
}
