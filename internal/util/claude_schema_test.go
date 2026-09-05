package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeToolInputSchema(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "root anyOf without type",
			input: `{
				"anyOf": [
					{"type":"object","properties":{"a":{"type":"string"}}},
					{"type":"object","properties":{"b":{"type":"integer"}}}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"a":{"type":"string"},
					"b":{"type":"integer"}
				}
			}`,
		},
		{
			name: "root oneOf keeps nested union",
			input: `{
				"type":"object",
				"properties":{
					"nested":{"oneOf":[{"type":"string"},{"type":"number"}]}
				},
				"oneOf":[
					{"properties":{"a":{"type":"string"}},"required":["a"]},
					{"properties":{"b":{"type":"string"}},"required":["b"]}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"nested":{"oneOf":[{"type":"string"},{"type":"number"}]},
					"a":{"type":"string"},
					"b":{"type":"string"}
				}
			}`,
		},
		{
			name: "root anyOf drops alternative required fields",
			input: `{
				"type":"object",
				"properties":{"a":{"type":"string"},"b":{"type":"string"}},
				"anyOf":[{"required":["a"]},{"required":["b"]}]
			}`,
			expected: `{
				"type":"object",
				"properties":{"a":{"type":"string"},"b":{"type":"string"}}
			}`,
		},
		{
			name: "root allOf merges properties and required fields",
			input: `{
				"type":"object",
				"properties":{"base":{"type":"boolean"}},
				"required":["base"],
				"allOf":[
					{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},
					{"properties":{"b":{"type":"integer"}},"required":["a","b"]}
				]
			}`,
			expected: `{
				"type":"object",
				"properties":{
					"base":{"type":"boolean"},
					"a":{"type":"string"},
					"b":{"type":"integer"}
				},
				"required":["base","a","b"]
			}`,
		},
		{
			name: "ordinary object schema",
			input: `{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`,
			expected: `{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`,
		},
		{
			name:     "invalid schema",
			input:    `{"type":`,
			expected: `{"type":"object","properties":{}}`,
		},
		{
			name:     "boolean schema",
			input:    `true`,
			expected: `{"type":"object","properties":{}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := NormalizeClaudeToolInputSchema([]byte(test.input))
			compareJSON(t, test.expected, string(actual))
		})
	}
}

func TestNormalizeClaudeToolInputSchemaResolvesNestedLocalRefs(t *testing.T) {
	input := `{
		"type":"object",
		"properties":{},
		"oneOf":[{"$ref":"#/$defs/view"},{"$ref":"#/$defs/createVariants"}],
		"$defs":{
			"view":{
				"type":"object",
				"additionalProperties":false,
				"properties":{"mode":{"type":"string","const":"view"},"id":{"type":"string"}},
				"required":["mode","id"]
			},
			"createVariants":{"oneOf":[{"$ref":"#/$defs/createCron"},{"$ref":"#/$defs/createHeartbeat"}]},
			"createCron":{
				"type":"object",
				"additionalProperties":false,
				"properties":{"mode":{"type":"string","const":"create"},"kind":{"type":"string","const":"cron"},"rrule":{"type":"string"}},
				"required":["mode","kind"]
			},
			"createHeartbeat":{
				"type":"object",
				"additionalProperties":false,
				"properties":{"mode":{"type":"string","const":"create"},"kind":{"type":"string","const":"heartbeat"},"rrule":{"type":"object","properties":{"frequency":{"oneOf":[{"type":"string"},{"type":"number"}]}}}},
				"required":["mode","kind"]
			}
		}
	}`

	actual := NormalizeClaudeToolInputSchema([]byte(input))
	root := gjson.ParseBytes(actual)
	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		if root.Get(keyword).Exists() {
			t.Fatalf("root %s must be removed: %s", keyword, string(actual))
		}
	}
	for _, property := range []string{"mode", "id", "kind", "rrule"} {
		if !root.Get("properties." + property).Exists() {
			t.Fatalf("merged schema is missing property %q: %s", property, string(actual))
		}
	}
	if got := root.Get("required.#").Int(); got != 1 || root.Get("required.0").String() != "mode" {
		t.Fatalf("required = %s, want only mode", root.Get("required").Raw)
	}
	if got := root.Get("properties.mode.enum.#").Int(); got != 2 {
		t.Fatalf("mode enum count = %d, want 2: %s", got, string(actual))
	}
	if !root.Get("properties.rrule.anyOf.1.properties.frequency.oneOf").Exists() {
		t.Fatalf("nested property combinator was not preserved: %s", string(actual))
	}
	if root.Get("additionalProperties").Bool() || !root.Get("additionalProperties").Exists() {
		t.Fatalf("additionalProperties should remain false: %s", string(actual))
	}
}
