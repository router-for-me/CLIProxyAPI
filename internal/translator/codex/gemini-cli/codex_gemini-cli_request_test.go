package geminiCLI

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiCLIRequestToCodex_PreservesSchemaPropertyNamedType(t *testing.T) {
	input := []byte(`{
		"request": {
			"tools": [
				{
					"functionDeclarations": [
						{
							"name": "ask_user",
							"description": "Ask the user one or more questions.",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"questions": {
										"type": "array",
										"items": {
											"type": "object",
											"properties": {
												"header": {
													"type": "string"
												},
												"type": {
													"default": "choice",
													"description": "Question type.",
													"enum": [
														"choice",
														"text",
														"yesno"
													],
													"type": "string"
												}
											},
											"required": [
												"question",
												"header",
												"type"
											]
										}
									}
								},
								"required": [
									"questions"
								]
							}
						}
					]
				}
			]
		}
	}`)

	out := ConvertGeminiCLIRequestToCodex("gpt-5.2", input, true)
	tool := gjson.GetBytes(out, "tools.0")
	if got := tool.Get("type").String(); got != "function" {
		t.Fatalf("expected tool type %q, got %q; output=%s", "function", got, string(out))
	}

	typeProperty := tool.Get("parameters.properties.questions.items.properties.type")
	if !typeProperty.IsObject() {
		t.Fatalf("expected schema property named type to stay an object; output=%s", string(out))
	}
	if got := typeProperty.Get("type").String(); got != "string" {
		t.Fatalf("expected schema property type %q, got %q; output=%s", "string", got, string(out))
	}
	if got := typeProperty.Get("default").String(); got != "choice" {
		t.Fatalf("expected default %q, got %q; output=%s", "choice", got, string(out))
	}
	if got := typeProperty.Get("enum.2").String(); got != "yesno" {
		t.Fatalf("expected enum value %q, got %q; output=%s", "yesno", got, string(out))
	}
}
