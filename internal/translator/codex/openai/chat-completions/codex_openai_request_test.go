package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToCodex_NormalizesStructuredOutputSchema(t *testing.T) {
	input := []byte(`{
		"model":"gpt-5",
		"messages":[{"role":"user","content":"return structured output"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"structured_output",
				"strict":true,
				"schema":{
					"type":"object",
					"additionalProperties":true,
					"properties":{
						"name":{"type":"string"},
						"meta":{
							"type":"object",
							"properties":{"count":{"type":"number"}}
						}
					},
					"required":["name"]
				}
			}
		}
	}`)

	out := ConvertOpenAIRequestToCodex("gpt-5", input, false)
	raw := string(out)

	if got := gjson.Get(raw, "text.format.schema.additionalProperties"); got.Type != gjson.False {
		t.Fatalf("root additionalProperties must be false, got %s", got.Raw)
	}
	if got := gjson.Get(raw, "text.format.schema.properties.meta.additionalProperties"); got.Type != gjson.False {
		t.Fatalf("nested additionalProperties must be false, got %s", got.Raw)
	}
}
