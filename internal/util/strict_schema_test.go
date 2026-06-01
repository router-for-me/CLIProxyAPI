package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCleanJSONSchemaForStrictUpstream_StripsOneOfAndNormalizesArrayItems(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"rewardTitleEffects": {
				"type": "array",
				"items": {
					"oneOf": [
						{"type": "string"},
						{"type": "object", "properties": {"title": {"type": "string"}}}
					]
				}
			}
		},
		"required": ["rewardTitleEffects"]
	}`

	result := CleanJSONSchemaForStrictUpstream(input)

	if gjson.Get(result, "properties.rewardTitleEffects.items.oneOf").Exists() {
		t.Fatalf("oneOf should be removed: %s", result)
	}
	if got := gjson.Get(result, "properties.rewardTitleEffects.items.type").String(); got == "" {
		t.Fatalf("items.type should be normalized: %s", result)
	}
	if got := gjson.Get(result, "additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("root additionalProperties should be false: %s", result)
	}
	if got := gjson.Get(result, "properties.rewardTitleEffects.items.properties.title").Exists(); got {
		if nested := gjson.Get(result, "properties.rewardTitleEffects.items.additionalProperties"); !nested.Exists() || nested.Bool() {
			t.Fatalf("nested object additionalProperties should be false: %s", result)
		}
	}
}

func TestCleanJSONSchemaForStrictUpstream_NormalizesNullArrayBits(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"sessions": {
				"type": "array",
				"items": null
			},
			"labels": {
				"type": ["array", "null"],
				"items": {"type": "string"}
			}
		},
		"required": null
	}`

	result := CleanJSONSchemaForStrictUpstream(input)

	if got := gjson.Get(result, "properties.sessions.items.type").String(); got == "" {
		t.Fatalf("sessions.items.type should be filled: %s", result)
	}
	if got := gjson.Get(result, "properties.labels.type").String(); got != "array" {
		t.Fatalf("expected labels.type=array, got %q: %s", got, result)
	}
	if gjson.Get(result, "required").Exists() {
		t.Fatalf("required should be removed when null: %s", result)
	}
}

func TestCleanJSONSchemaForStrictUpstream_EmptyFallsBackToObject(t *testing.T) {
	result := CleanJSONSchemaForStrictUpstream("")
	if got := gjson.Get(result, "type").String(); got != "object" {
		t.Fatalf("expected object fallback, got %q: %s", got, result)
	}
	if !gjson.Get(result, "properties").IsObject() {
		t.Fatalf("expected object fallback properties: %s", result)
	}
	if got := gjson.Get(result, "additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("expected object fallback additionalProperties=false: %s", result)
	}
}

func TestCleanJSONSchemaForStrictUpstream_AddsAdditionalPropertiesFalseRecursively(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"caption": {"type": "string"},
			"metadata": {
				"type": "object",
				"properties": {
					"source": {"type": "string"}
				}
			}
		}
	}`

	result := CleanJSONSchemaForStrictUpstream(input)

	if got := gjson.Get(result, "additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("root additionalProperties should be false: %s", result)
	}
	if got := gjson.Get(result, "properties.metadata.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("nested additionalProperties should be false: %s", result)
	}
}

func TestCleanJSONSchemaForStrictUpstream_NormalizesScalarPropertySchemas(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"type": "object",
			"required": "array",
			"additionalProperties": "object",
			"metadata": {
				"type": "object",
				"properties": {
					"tags": ["array", "null"]
				}
			}
		},
		"additionalProperties": "object",
		"required": null
	}`

	result := CleanJSONSchemaForStrictUpstream(input)

	if got := gjson.Get(result, "properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, result)
	}
	if !gjson.Get(result, "properties.type.properties").IsObject() {
		t.Fatalf("properties.type should include object properties: %s", result)
	}
	if got := gjson.Get(result, "properties.required.type").String(); got != "array" {
		t.Fatalf("properties.required should be an array schema, got %q: %s", got, result)
	}
	if got := gjson.Get(result, "properties.required.items.type").String(); got != "string" {
		t.Fatalf("properties.required.items should default to string, got %q: %s", got, result)
	}
	if got := gjson.Get(result, "properties.additionalProperties.type").String(); got != "object" {
		t.Fatalf("property named additionalProperties should be an object schema, got %q: %s", got, result)
	}
	if got := gjson.Get(result, "properties.metadata.properties.tags.type").String(); got != "array" {
		t.Fatalf("nullable scalar array property should normalize to array, got %q: %s", got, result)
	}
	if got := gjson.Get(result, "additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("root additionalProperties should be strict false: %s", result)
	}
	if gjson.Get(result, "required").Exists() {
		t.Fatalf("required=null should be removed: %s", result)
	}
}

func TestCleanJSONSchemaForOpenAIStructuredOutput_RequiresAllObjectProperties(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"explicit": {"type": "boolean"},
			"metadata": {
				"type": "object",
				"properties": {
					"source": {"type": "string"},
					"confidence": {"type": ["number", "null"]}
				},
				"required": ["source"]
			}
		},
		"required": ["name"]
	}`

	result := CleanJSONSchemaForOpenAIStructuredOutput(input)

	required := gjson.Get(result, "required").Array()
	if len(required) != 3 {
		t.Fatalf("root required should include every property, got %s in %s", gjson.Get(result, "required").Raw, result)
	}
	for _, key := range []string{"explicit", "metadata", "name"} {
		if !containsGJSONString(required, key) {
			t.Fatalf("root required missing %q: %s", key, result)
		}
	}
	nestedRequired := gjson.Get(result, "properties.metadata.required").Array()
	if len(nestedRequired) != 2 {
		t.Fatalf("nested required should include every property, got %s in %s", gjson.Get(result, "properties.metadata.required").Raw, result)
	}
	for _, key := range []string{"confidence", "source"} {
		if !containsGJSONString(nestedRequired, key) {
			t.Fatalf("nested required missing %q: %s", key, result)
		}
	}
}

func containsGJSONString(values []gjson.Result, want string) bool {
	for _, value := range values {
		if value.String() == want {
			return true
		}
	}
	return false
}
