package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeStructuredOutputSchema_ObjectNodesSetAdditionalPropertiesFalse(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{
			"name":{"type":"string"},
			"meta":{
				"type":"object",
				"properties":{"count":{"type":"number"}}
			}
		},
		"required":["name"]
	}`

	out := NormalizeStructuredOutputSchema(input)

	if got := gjson.Get(out, "additionalProperties"); got.Type != gjson.False {
		t.Fatalf("root additionalProperties must be false, got %s", got.Raw)
	}
	if got := gjson.Get(out, "properties.meta.additionalProperties"); got.Type != gjson.False {
		t.Fatalf("nested additionalProperties must be false, got %s", got.Raw)
	}
}

func TestNormalizeStructuredOutputSchema_OverwriteTrueToFalse(t *testing.T) {
	input := `{
		"type":"object",
		"additionalProperties":true,
		"properties":{
			"payload":{
				"type":"object",
				"additionalProperties":{"type":"string"},
				"properties":{"x":{"type":"string"}}
			}
		}
	}`

	out := NormalizeStructuredOutputSchema(input)

	if got := gjson.Get(out, "additionalProperties"); got.Type != gjson.False {
		t.Fatalf("root additionalProperties must be false, got %s", got.Raw)
	}
	if got := gjson.Get(out, "properties.payload.additionalProperties"); got.Type != gjson.False {
		t.Fatalf("nested additionalProperties must be false, got %s", got.Raw)
	}
}

func TestNormalizeStructuredOutputSchema_InvalidJSONReturnsOriginal(t *testing.T) {
	input := `{"type":"object"`
	out := NormalizeStructuredOutputSchema(input)
	if out != input {
		t.Fatalf("invalid JSON should be returned unchanged")
	}
}
