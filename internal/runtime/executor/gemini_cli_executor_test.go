package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCleanGeminiCLIRequestSchemasFlattensFunctionDeclarationTypeArray(t *testing.T) {
	input := []byte(`{
		"request": {
			"tools": [
				{
					"function_declarations": [
						{
							"name": "wecom_mcp",
							"parameters": {
								"type": "object",
								"properties": {
									"args": {
										"description": "call args",
										"type": ["string", "object"]
									}
								}
							}
						}
					]
				},
				{
					"functionDeclarations": [
						{
							"name": "camel_tool",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"value": {
										"type": ["integer", "string"]
									}
								}
							}
						}
					]
				}
			],
			"nonSchema": {
				"type": ["string", "object"]
			}
		}
	}`)

	out := cleanGeminiCLIRequestSchemas(input)

	argsType := gjson.GetBytes(out, "request.tools.0.function_declarations.0.parameters.properties.args.type")
	if argsType.String() != "string" {
		t.Fatalf("args.type = %s, want string; body=%s", argsType.Raw, string(out))
	}
	argsDesc := gjson.GetBytes(out, "request.tools.0.function_declarations.0.parameters.properties.args.description").String()
	if !strings.Contains(argsDesc, "Accepts: string | object") {
		t.Fatalf("args.description = %q, want accepted type hint", argsDesc)
	}

	valueType := gjson.GetBytes(out, "request.tools.1.functionDeclarations.0.parametersJsonSchema.properties.value.type")
	if valueType.String() != "integer" {
		t.Fatalf("value.type = %s, want integer; body=%s", valueType.Raw, string(out))
	}
	valueDesc := gjson.GetBytes(out, "request.tools.1.functionDeclarations.0.parametersJsonSchema.properties.value.description").String()
	if !strings.Contains(valueDesc, "Accepts: integer | string") {
		t.Fatalf("value.description = %q, want accepted type hint", valueDesc)
	}

	if nonSchema := gjson.GetBytes(out, "request.nonSchema.type"); !nonSchema.IsArray() {
		t.Fatalf("request.nonSchema.type should be preserved outside schema paths, got %s", nonSchema.Raw)
	}
}
