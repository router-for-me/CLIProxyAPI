package claude

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func collectCodexEventData(chunks []string, eventName string) []string {
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

func TestConvertCodexResponseToClaude_SanitizesStreamingToolUseID(t *testing.T) {
	requestJSON := []byte(`{"tools": [{"name": "fs.readFile"}]}`)
	responseJSON := []byte(`data: {
		"type": "response.output_item.added",
		"item": {
			"type": "function_call",
			"call_id": "call.fs.read",
			"name": "fs.readFile"
		}
	}`)

	var param any
	chunks := ConvertCodexResponseToClaude(context.Background(), "gpt-5-codex", requestJSON, requestJSON, responseJSON, &param)
	payloads := collectCodexEventData(chunks, "content_block_start")
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

func TestConvertCodexResponseToClaudeNonStream_SanitizesToolUseID(t *testing.T) {
	requestJSON := []byte(`{"tools": [{"name": "fs.readFile"}]}`)
	responseJSON := []byte(`{
		"type": "response.completed",
		"response": {
			"id": "resp_1",
			"model": "gpt-5-codex",
			"output": [{
				"type": "function_call",
				"call_id": "call.fs.read",
				"name": "fs.readFile",
				"arguments": "{\"path\":\"a.txt\"}"
			}],
			"usage": {"input_tokens": 1, "output_tokens": 1},
			"stop_reason": "tool_use"
		}
	}`)

	output := ConvertCodexResponseToClaudeNonStream(context.Background(), "gpt-5-codex", requestJSON, nil, responseJSON, nil)
	id := gjson.Get(output, "content.0.id").String()
	if id == "" {
		t.Fatal("Expected non-empty tool_use id")
	}
	if strings.Contains(id, ".") {
		t.Fatalf("Expected sanitized tool_use id without dots, got %q", id)
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
		t.Fatalf("Expected tool_use id to match Claude regex, got %q", id)
	}
	if got := gjson.Get(output, "content.0.name").String(); got != "fs.readFile" {
		t.Fatalf("Expected tool name %q, got %q", "fs.readFile", got)
	}
}
