package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravitySanitizesTrueSubschemas(t *testing.T) {
	input := []byte(`{
		"messages": [{"role": "user", "content": "test"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "test_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"tuple": {
							"items": [{
								"type": "object",
								"properties": {
									"screenshot_id": true
								},
								"additionalProperties": false
							}]
						}
					}
				}
			}
		}]
	}`)

	output := ConvertOpenAIRequestToAntigravity("claude-opus-4-6-thinking", input, false)

	trueSchema := gjson.GetBytes(
		output,
		"request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.tuple.items.0.properties.screenshot_id",
	)
	if trueSchema.Raw != "{}" {
		t.Fatalf("expected true subschema to be converted to {}, got %s", trueSchema.Raw)
	}

	additionalProperties := gjson.GetBytes(
		output,
		"request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.tuple.items.0.additionalProperties",
	)
	if additionalProperties.Raw != "false" {
		t.Fatalf("expected additionalProperties false to be preserved, got %s", additionalProperties.Raw)
	}
}

func TestConvertOpenAIRequestToAntigravityPreservesLargeSchemaNumbers(t *testing.T) {
	input := []byte(`{
		"messages": [{"role": "user", "content": "test"}],
		"tools": [{
			"type": "function",
			"function": {
				"name": "test_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"id": {
							"type": "integer",
							"enum": [9007199254740993]
						},
						"enabled": true
					}
				}
			}
		}]
	}`)

	output := ConvertOpenAIRequestToAntigravity("claude-opus-4-6-thinking", input, false)

	value := gjson.GetBytes(
		output,
		"request.tools.0.functionDeclarations.0.parametersJsonSchema.properties.id.enum.0",
	)
	if value.Raw != "9007199254740993" {
		t.Fatalf("expected large integer to be preserved, got %s", value.Raw)
	}
}
