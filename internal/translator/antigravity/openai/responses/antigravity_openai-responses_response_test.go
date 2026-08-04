package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertAntigravityResponseToOpenAIResponsesNonStream_PreservesOpenAITools(
	t *testing.T,
) {
	// Original OpenAI request without the Antigravity "request" wrapper.
	originalReq := []byte(`{
		"model": "gemini-3.6-flash",
		"tools": [
			{
				"type": "function",
				"name": "get_weather",
				"description": "Get current weather",
				"parameters": {
					"type": "object",
					"properties": {
						"location": {
							"type": "string"
						}
					},
					"required": ["location"],
					"additionalProperties": false
				},
				"strict": true
			}
		]
	}`)

	// Translated Antigravity/Gemini request wrapped in "request".
	translatedReq := []byte(`{
		"request": {
			"tools": [
				{
					"functionDeclarations": [
						{
							"name": "get_weather",
							"description": "Get current weather",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"location": {
										"type": "string"
									}
								},
								"required": ["location"]
							}
						}
					]
				}
			]
		}
	}`)

	// Antigravity upstream response wrapped in "response".
	rawResp := []byte(`{
		"response": {
			"candidates": [
				{
					"content": {
						"parts": [
							{
								"functionCall": {
									"name": "get_weather",
									"args": {
										"location": "Tokyo"
									}
								}
							}
						],
						"role": "model"
					},
					"finishReason": "STOP"
				}
			]
		}
	}`)

	out := ConvertAntigravityResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gemini-3.6-flash",
		originalReq,
		translatedReq,
		rawResp,
		nil,
	)

	if !gjson.ValidBytes(out) {
		t.Fatalf("expected valid JSON response, got: %s", out)
	}

	tool := gjson.GetBytes(out, "tools.0")
	if !tool.Exists() {
		t.Fatalf("expected tools[0] in response, got: %s", out)
	}

	if got := tool.Get("type").String(); got != "function" {
		t.Fatalf(
			"expected tools[0].type to be %q, got %q; response: %s",
			"function",
			got,
			out,
		)
	}

	if got := tool.Get("name").String(); got != "get_weather" {
		t.Fatalf(
			"expected tools[0].name to be %q, got %q; response: %s",
			"get_weather",
			got,
			out,
		)
	}

	if tool.Get("functionDeclarations").Exists() {
		t.Fatalf(
			"Gemini-native functionDeclarations leaked into OpenAI response: %s",
			tool.Raw,
		)
	}

	if got := tool.Get("parameters.type").String(); got != "object" {
		t.Fatalf(
			"expected OpenAI parameters schema, got: %s",
			tool.Raw,
		)
	}

	if !tool.Get("strict").Bool() {
		t.Fatalf("expected strict=true to be preserved: %s", tool.Raw)
	}

	functionCall := gjson.GetBytes(
		out,
		`output.#(type=="function_call")`,
	)

	if !functionCall.Exists() {
		t.Fatalf(
			"expected OpenAI function_call output, got: %s",
			out,
		)
	}

	if got := functionCall.Get("name").String(); got != "get_weather" {
		t.Fatalf(
			"expected function call name %q, got %q",
			"get_weather",
			got,
		)
	}
}
