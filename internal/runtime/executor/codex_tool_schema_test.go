package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexToolSchemasCleansClaudeCodeScalarProperties(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5.3-codex",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{
			"type":"function",
			"name":"mcp__pencil__replace_all_matching_properties",
			"parameters":{
				"type":"object",
				"properties":{
					"type":"object",
					"required":"array",
					"additionalProperties":"object"
				},
				"additionalProperties":"object",
				"required":null
			}
		}]
	}`)

	out := normalizeCodexToolSchemas(payload)

	if got := gjson.GetBytes(out, "tools.0.parameters.properties.type.type").String(); got != "object" {
		t.Fatalf("properties.type should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.required.type").String(); got != "array" {
		t.Fatalf("properties.required should be an array schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.properties.additionalProperties.type").String(); got != "object" {
		t.Fatalf("property named additionalProperties should be an object schema, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.parameters.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("root additionalProperties should be strict false: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.0.parameters.required").Exists() {
		t.Fatalf("required=null should be removed: %s", string(out))
	}
}

func TestNormalizeCodexToolSchemasCleansNestedFunctionShape(t *testing.T) {
	payload := []byte(`{
		"tools":[{
			"type":"function",
			"function":{
				"name":"inspect",
				"parameters":{
					"type":"object",
					"properties":{"metadata":"object"}
				}
			}
		}]
	}`)

	out := normalizeCodexToolSchemas(payload)

	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.metadata.type").String(); got != "object" {
		t.Fatalf("nested function parameters should be cleaned, got %q: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("nested function parameters should be strict: %s", string(out))
	}
}
