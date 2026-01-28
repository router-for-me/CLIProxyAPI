package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestFixCodexToolSchemas_AddsMissingItems(t *testing.T) {
	input := `{"tools":[{"type":"function","parameters":{"type":"object","properties":{"options":{"type":"array"}}}}]}`
	result := FixCodexToolSchemas([]byte(input))

	items := gjson.GetBytes(result, "tools.0.parameters.properties.options.items")
	if !items.Exists() {
		t.Error("expected items to be added to array schema")
	}
}

func TestFixCodexToolSchemas_PreservesExistingItems(t *testing.T) {
	input := `{"tools":[{"type":"function","parameters":{"type":"object","properties":{"options":{"type":"array","items":{"type":"string"}}}}}]}`
	result := FixCodexToolSchemas([]byte(input))

	itemsType := gjson.GetBytes(result, "tools.0.parameters.properties.options.items.type").String()
	if itemsType != "string" {
		t.Errorf("expected existing items to be preserved, got type=%s", itemsType)
	}
}

func TestFixCodexToolSchemas_HandlesAnyOf(t *testing.T) {
	input := `{"tools":[{"type":"function","parameters":{"anyOf":[{"type":"array"}]}}]}`
	result := FixCodexToolSchemas([]byte(input))

	items := gjson.GetBytes(result, "tools.0.parameters.anyOf.0.items")
	if !items.Exists() {
		t.Error("expected items to be added to array schema inside anyOf")
	}
}

func TestFixCodexToolSchemas_NoTools(t *testing.T) {
	input := `{"model":"gpt-5"}`
	result := FixCodexToolSchemas([]byte(input))

	if string(result) != input {
		t.Error("expected unchanged output when no tools present")
	}
}

func TestFixCodexToolSchemas_ChatCompletionsFormat(t *testing.T) {
	input := `{"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object","properties":{"items":{"type":"array"}}}}}]}`
	result := FixCodexToolSchemas([]byte(input))

	items := gjson.GetBytes(result, "tools.0.function.parameters.properties.items.items")
	if !items.Exists() {
		t.Error("expected items to be added to array schema in function.parameters")
	}
}

func TestFixCodexToolSchemas_NullableArrayType(t *testing.T) {
	input := `{"tools":[{"type":"function","parameters":{"type":"object","properties":{"data":{"type":["array","null"]}}}}]}`
	result := FixCodexToolSchemas([]byte(input))

	items := gjson.GetBytes(result, "tools.0.parameters.properties.data.items")
	if !items.Exists() {
		t.Error("expected items to be added to nullable array schema")
	}
}

func TestFixCodexToolSchemas_NullItems(t *testing.T) {
	input := `{"tools":[{"type":"function","parameters":{"type":"object","properties":{"list":{"type":"array","items":null}}}}]}`
	result := FixCodexToolSchemas([]byte(input))

	items := gjson.GetBytes(result, "tools.0.parameters.properties.list.items")
	if items.Type == gjson.Null {
		t.Error("expected null items to be replaced with empty object")
	}
}
