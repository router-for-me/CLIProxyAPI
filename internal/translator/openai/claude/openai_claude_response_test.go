package claude

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func collectOpenAIEventData(chunks []string, eventName string) []string {
	var payloads []string
	for _, chunk := range chunks {
		currentEvent := ""
		for _, line := range strings.Split(chunk, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				currentEvent = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && currentEvent == eventName:
				payloads = append(payloads, strings.TrimPrefix(line, "data: "))
			}
		}
	}
	return payloads
}

func TestConvertOpenAIResponseToClaude_SanitizesStreamingToolUseID(t *testing.T) {
	requestJSON := []byte(`{"stream": true, "tools": [{"name": "fs.readFile"}]}`)
	responseJSON := []byte(`data: {
		"id": "chatcmpl_1",
		"model": "gpt-4.1",
		"created": 123,
		"choices": [{
			"delta": {
				"tool_calls": [{
					"index": 0,
					"id": "call.fs.read",
					"function": {"name": "fs.readFile", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			}
		}]
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(context.Background(), "gpt-4.1", requestJSON, requestJSON, responseJSON, &param)
	payloads := collectOpenAIEventData(chunks, "content_block_start")
	if len(payloads) == 0 {
		t.Fatal("Expected content_block_start event")
	}

	payload := payloads[len(payloads)-1]
	id := gjson.Get(payload, "content_block.id").String()
	if id == "" {
		t.Fatal("Expected non-empty tool_use id")
	}
	if strings.Contains(id, ".") {
		t.Fatalf("Expected sanitized tool_use id without dots, got %q", id)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
		t.Fatalf("Expected tool_use id to match Claude regex, got %q", id)
	}
	if got := gjson.Get(payload, "content_block.name").String(); got != "fs.readFile" {
		t.Fatalf("Expected tool name %q, got %q", "fs.readFile", got)
	}
}

func TestConvertOpenAIResponseToClaude_SanitizesNonStreamingToolUseID(t *testing.T) {
	requestJSON := []byte(`{"stream": false}`)
	responseJSON := []byte(`data: {
		"id": "chatcmpl_1",
		"model": "gpt-4.1",
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call.fs.read",
					"type": "function",
					"function": {"name": "fs.readFile", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(context.Background(), "gpt-4.1", requestJSON, requestJSON, responseJSON, &param)
	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk, got %d", len(chunks))
	}

	id := gjson.Get(chunks[0], "content.0.id").String()
	if id == "" {
		t.Fatal("Expected non-empty tool_use id")
	}
	if strings.Contains(id, ".") {
		t.Fatalf("Expected sanitized tool_use id without dots, got %q", id)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
		t.Fatalf("Expected tool_use id to match Claude regex, got %q", id)
	}
	if got := gjson.Get(chunks[0], "content.0.name").String(); got != "fs.readFile" {
		t.Fatalf("Expected tool name %q, got %q", "fs.readFile", got)
	}
}
