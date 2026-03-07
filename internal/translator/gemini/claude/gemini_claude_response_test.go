package claude

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func collectEventData(chunks []string, eventName string) []string {
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

func TestConvertGeminiResponseToClaude_SanitizesToolUseID(t *testing.T) {
	requestJSON := []byte(`{"messages":[]}`)
	responseJSON := []byte(`{
		"responseId": "resp_1",
		"modelVersion": "gemini-2.5-pro",
		"candidates": [{
			"content": {
				"parts": [{"functionCall": {"name": "fs.readFile", "args": {"path": "a.txt"}}}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1}
	}`)

	var param any
	chunks := ConvertGeminiResponseToClaude(context.Background(), "gemini-2.5-pro", requestJSON, requestJSON, responseJSON, &param)
	payloads := collectEventData(chunks, "content_block_start")
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

func TestConvertGeminiResponseToClaudeNonStream_SanitizesToolUseID(t *testing.T) {
	requestJSON := []byte(`{"messages":[]}`)
	responseJSON := []byte(`{
		"responseId": "resp_1",
		"modelVersion": "gemini-2.5-pro",
		"candidates": [{
			"content": {
				"parts": [{"functionCall": {"name": "fs.readFile", "args": {"path": "a.txt"}}}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1}
	}`)

	output := ConvertGeminiResponseToClaudeNonStream(context.Background(), "gemini-2.5-pro", requestJSON, requestJSON, responseJSON, nil)
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
