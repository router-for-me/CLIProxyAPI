package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponseSchema_MakesOptionalPropertiesNullableAndRequired(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"age": {"type": "integer"}
		},
		"required": ["name"]
	}`

	result := NormalizeOpenAIResponseSchema(input)

	required := gjson.Get(result, "required").Array()
	if len(required) != 2 {
		t.Fatalf("required length = %d, want 2; schema=%s", len(required), result)
	}
	if !gjson.Get(result, `required.#(=="name")`).Exists() {
		t.Fatalf("required missing name; schema=%s", result)
	}
	if !gjson.Get(result, `required.#(=="age")`).Exists() {
		t.Fatalf("required missing age; schema=%s", result)
	}

	ageType := gjson.Get(result, "properties.age.type").Array()
	if len(ageType) != 2 {
		t.Fatalf("age type length = %d, want 2; schema=%s", len(ageType), result)
	}
	if ageType[0].String() != "integer" || ageType[1].String() != "null" {
		t.Fatalf("age type = %s, want [integer null]; schema=%s", gjson.Get(result, "properties.age.type").Raw, result)
	}
}
