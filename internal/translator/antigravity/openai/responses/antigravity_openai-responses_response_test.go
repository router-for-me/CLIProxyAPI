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

func TestConvertAntigravityResponseToOpenAIResponsesNonStream_WebSearch(t *testing.T) {
	origReq := []byte(`{
		"model": "gemini-3.8-flash-high",
		"input": "Go release history",
		"tools": [{"type": "web_search"}]
	}`)

	antigravityResp := []byte(`{
		"response": {
			"responseId": "ag-search-resp-1",
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{
						"text": "Go 1.27 was released recently."
					}]
				},
				"groundingMetadata": {
					"webSearchQueries": ["Go release history"],
					"groundingChunks": [
						{"web": {"uri": "https://go.dev/doc/devel/release", "title": "Release History"}}
					],
					"groundingSupports": [{
						"groundingChunkIndices": [0],
						"segment": {"startIndex": 0, "endIndex": 7, "text": "Go 1.27"}
					}]
				}
			}],
			"usageMetadata": {
				"promptTokenCount": 20,
				"candidatesTokenCount": 10,
				"totalTokenCount": 30
			}
		}
	}`)

	output := ConvertAntigravityResponseToOpenAIResponsesNonStream(
		context.Background(),
		"gemini-3.8-flash-high",
		origReq,
		nil,
		antigravityResp,
		nil,
	)

	parsed := gjson.ParseBytes(output)
	outputs := parsed.Get("output").Array()
	if len(outputs) != 2 {
		t.Fatalf("expected 2 outputs, got %d: %s", len(outputs), output)
	}

	ws := outputs[0]
	if ws.Get("type").String() != "web_search_call" {
		t.Fatalf("expected output.0.type web_search_call, got %q", ws.Get("type").String())
	}
	if ws.Get("action.query").String() != "Go release history" {
		t.Fatalf("expected action.query 'Go release history', got %q", ws.Get("action.query").String())
	}
	if ws.Get("action.sources.0.url").String() != "https://go.dev/doc/devel/release" {
		t.Fatalf("expected action.sources.0.url, got %q", ws.Get("action.sources.0.url").String())
	}

	msg := outputs[1]
	if msg.Get("type").String() != "message" {
		t.Fatalf("expected output.1.type message, got %q", msg.Get("type").String())
	}
	citation := msg.Get("content.0.annotations.0")
	if citation.Get("type").String() != "url_citation" {
		t.Fatalf("expected annotation type url_citation, got %q", citation.Get("type").String())
	}
	if citation.Get("url").String() != "https://go.dev/doc/devel/release" {
		t.Fatalf("expected citation url, got %q", citation.Get("url").String())
	}
	if citation.Get("title").String() != "Release History" {
		t.Fatalf("expected citation title, got %q", citation.Get("title").String())
	}

	if parsed.Get("tool_usage.web_search.num_requests").Int() != 1 {
		t.Fatalf("expected tool_usage.web_search.num_requests = 1, got %d", parsed.Get("tool_usage.web_search.num_requests").Int())
	}
}
