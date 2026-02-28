package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaude_StreamToolStartEmittedOnceAndNameCanonicalized(t *testing.T) {
	originalRequest := `{
		"stream": true,
		"tools": [
			{
				"name": "Bash",
				"description": "run shell",
				"input_schema": {"type":"object","properties":{"command":{"type":"string"}}}
			}
		]
	}`

	chunks := []string{
		`data: {"id":"chatcmpl-1","model":"m","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":""}}]}}]}`,
		`data: {"id":"chatcmpl-1","model":"m","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\"command\":\"pwd\"}"}}]}}]}`,
		`data: {"id":"chatcmpl-1","model":"m","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"chatcmpl-1","model":"m","created":1,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`data: [DONE]`,
	}

	var param any
	var outputs []string
	for _, chunk := range chunks {
		out := ConvertOpenAIResponseToClaude(context.Background(), "m", []byte(originalRequest), nil, []byte(chunk), &param)
		outputs = append(outputs, out...)
	}

	joined := strings.Join(outputs, "")
	if got := strings.Count(joined, `"content_block":{"type":"tool_use"`); got != 1 {
		t.Fatalf("expected exactly 1 tool_use content_block_start, got %d\noutput:\n%s", got, joined)
	}

	if strings.Contains(joined, `"name":""`) {
		t.Fatalf("tool_use block should not have empty name\noutput:\n%s", joined)
	}

	if !strings.Contains(joined, `"name":"Bash"`) {
		t.Fatalf("expected canonical tool name Bash in stream output\noutput:\n%s", joined)
	}
}

func TestConvertOpenAIResponseToClaudeNonStream_CanonicalizesToolName(t *testing.T) {
	originalRequest := `{
		"tools": [
			{"name": "Bash", "input_schema": {"type":"object","properties":{"command":{"type":"string"}}}}
		]
	}`

	openAIResponse := `{
		"id":"chatcmpl-1",
		"model":"m",
		"choices":[
			{
				"finish_reason":"tool_calls",
				"message":{
					"content":"",
					"tool_calls":[
						{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"pwd\"}"}}
					]
				}
			}
		],
		"usage":{"prompt_tokens":10,"completion_tokens":2}
	}`

	var param any
	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "m", []byte(originalRequest), nil, []byte(openAIResponse), &param)
	result := gjson.Parse(out)

	if got := result.Get("content.0.type").String(); got != "tool_use" {
		t.Fatalf("expected first content block type tool_use, got %q", got)
	}
	if got := result.Get("content.0.name").String(); got != "Bash" {
		t.Fatalf("expected canonical tool name %q, got %q", "Bash", got)
	}
}
