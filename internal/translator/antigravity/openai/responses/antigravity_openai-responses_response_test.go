package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertAntigravityResponseToOpenAIResponsesNonStream_PreservesOpenAITools(t *testing.T) {
	originalRequest := []byte(`{
        "model": "gemini-3.5-flash-low",
        "input": "Call get_weather for Tokyo.",
        "tools": [{
            "type": "function",
            "name": "get_weather",
            "description": "Get weather for a city",
            "parameters": {
                "type": "object",
                "properties": {"city": {"type": "string"}},
                "required": ["city"]
            }
        }],
        "tool_choice": "required"
    }`)
	translatedRequest := []byte(`{
        "request": {
            "model": "gemini-3.5-flash-low",
            "tools": [{
                "functionDeclarations": [{
                    "name": "get_weather",
                    "description": "Get weather for a city",
                    "parameters": {
                        "type": "OBJECT",
                        "properties": {"city": {"type": "STRING"}},
                        "required": ["city"]
                    }
                }]
            }]
        }
    }`)
	rawResponse := []byte(`{
        "response": {
            "responseId": "antigravity-tool-response",
            "candidates": [{
                "content": {
                    "parts": [{
                        "functionCall": {
                            "name": "get_weather",
                            "args": {"city": "Tokyo"}
                        }
                    }]
                },
                "finishReason": "STOP"
            }]
        }
    }`)

	output := ConvertAntigravityResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gemini-3.5-flash-low",
		originalRequest,
		translatedRequest,
		rawResponse,
		nil,
	)

	if !gjson.ValidBytes(output) {
		t.Fatalf("converter returned invalid JSON: %s", output)
	}
	if got := gjson.GetBytes(output, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function; output=%s", got, output)
	}
	if gjson.GetBytes(output, "tools.0.functionDeclarations").Exists() {
		t.Fatalf("OpenAI response contains Gemini-native functionDeclarations: %s", output)
	}
	if got := gjson.GetBytes(output, "output.0.type").String(); got != "function_call" {
		t.Fatalf("output.0.type = %q, want function_call; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.name").String(); got != "get_weather" {
		t.Fatalf("output.0.name = %q, want get_weather; output=%s", got, output)
	}
	arguments := gjson.GetBytes(output, "output.0.arguments").String()
	if !gjson.Valid(arguments) || gjson.Get(arguments, "city").String() != "Tokyo" {
		t.Fatalf("output.0.arguments = %q, want JSON arguments with city Tokyo; output=%s", arguments, output)
	}
}

func TestConvertAntigravityResponseToOpenAIResponses_RestoresAdditionalNamespaceCustomToolCall(t *testing.T) {
	originalRequest := []byte(`{
		"model": "gemini-3.5-flash-low",
		"input": [{
			"type": "additional_tools",
			"tools": [{
				"type": "namespace",
				"name": "functions",
				"tools": [{"type": "custom", "name": "exec"}]
			}]
		}]
	}`)
	rawResponse := []byte(`{
		"response": {
			"responseId": "antigravity-custom-response",
			"candidates": [{
				"content": {
					"parts": [{
						"functionCall": {
							"name": "functions__exec",
							"args": {"input": "pwd"}
						}
					}]
				},
				"finishReason": "STOP"
			}]
		}
	}`)

	output := ConvertAntigravityResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gemini-3.5-flash-low",
		originalRequest,
		nil,
		rawResponse,
		nil,
	)

	if !gjson.ValidBytes(output) {
		t.Fatalf("invalid JSON output: %s", output)
	}
	if got := gjson.GetBytes(output, "output.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("output.0.type = %q, want custom_tool_call; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "output.0.name").String(); got != "exec" {
		t.Fatalf("output.0.name = %q, want exec", got)
	}
	if got := gjson.GetBytes(output, "output.0.namespace").String(); got != "functions" {
		t.Fatalf("output.0.namespace = %q, want functions", got)
	}
	if got := gjson.GetBytes(output, "output.0.input").String(); got != "pwd" {
		t.Fatalf("output.0.input = %q, want pwd", got)
	}
}
