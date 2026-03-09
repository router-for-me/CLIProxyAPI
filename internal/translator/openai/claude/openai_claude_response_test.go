package claude

import (
	"context"
	"fmt"
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

func TestOpenAIToolCallIDRoundTripsThroughClaude(t *testing.T) {
	originalID := "call.fs:read"
	requestJSON := []byte(`{"stream": false}`)
	responseJSON := []byte(`data: {
		"id": "chatcmpl_1",
		"model": "gpt-4.1",
		"choices": [{
			"message": {
				"tool_calls": [{
					"id": "call.fs:read",
					"type": "function",
					"function": {"name": "fs.readFile", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(context.Background(), "gpt-4.1", requestJSON, requestJSON, responseJSON, &param)
	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk, got %d", len(chunks))
	}

	claudeToolUseID := gjson.Get(chunks[0], "content.0.id").String()
	if claudeToolUseID == "" {
		t.Fatal("Expected Claude tool_use id")
	}
	if claudeToolUseID == originalID {
		t.Fatal("Expected invalid OpenAI id to be transformed for Claude")
	}

	roundTripRequest := fmt.Sprintf(`{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": %q, "name": "fs.readFile", "input": {"path": "a.txt"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": %q, "content": "ok"}
				]
			}
		]
	}`, claudeToolUseID, claudeToolUseID)

	translated := ConvertClaudeRequestToOpenAI("test-model", []byte(roundTripRequest), false)
	messages := gjson.ParseBytes(translated).Get("messages").Array()
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages after round-trip, got %d", len(messages))
	}
	if got := messages[0].Get("tool_calls.0.id").String(); got != originalID {
		t.Fatalf("Expected assistant tool_call id %q after round-trip, got %q", originalID, got)
	}
	if got := messages[1].Get("tool_call_id").String(); got != originalID {
		t.Fatalf("Expected tool result id %q after round-trip, got %q", originalID, got)
	}
}
