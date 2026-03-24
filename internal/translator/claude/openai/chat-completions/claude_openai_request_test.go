package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaude_ToolResultTextAndBase64Image(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{"type": "text", "text": "tool ok"},
					{
						"type": "image_url",
						"image_url": {
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolResult := messages[1].Get("content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected content[0].type %q, got %q", "tool_result", got)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_1" {
		t.Fatalf("Expected tool_use_id %q, got %q", "call_1", got)
	}

	toolContent := toolResult.Get("content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected first tool_result part type %q, got %q", "text", got)
	}
	if got := toolContent.Get("0.text").String(); got != "tool ok" {
		t.Fatalf("Expected first tool_result part text %q, got %q", "tool ok", got)
	}
	if got := toolContent.Get("1.type").String(); got != "image" {
		t.Fatalf("Expected second tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("1.source.type").String(); got != "base64" {
		t.Fatalf("Expected image source type %q, got %q", "base64", got)
	}
	if got := toolContent.Get("1.source.media_type").String(); got != "image/png" {
		t.Fatalf("Expected image media type %q, got %q", "image/png", got)
	}
	if got := toolContent.Get("1.source.data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("Unexpected base64 image data: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_ToolResultURLImageOnly(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{
						"type": "image_url",
						"image_url": {
							"url": "https://example.com/tool.png"
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolContent := messages[1].Get("content.0.content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "image" {
		t.Fatalf("Expected tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("0.source.type").String(); got != "url" {
		t.Fatalf("Expected image source type %q, got %q", "url", got)
	}
	if got := toolContent.Get("0.source.url").String(); got != "https://example.com/tool.png" {
		t.Fatalf("Unexpected image URL: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemRoleBecomesTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system")
	if !system.IsArray() {
		t.Fatalf("Expected top-level system array, got %s", system.Raw)
	}
	if len(system.Array()) != 1 {
		t.Fatalf("Expected 1 system block, got %d. System: %s", len(system.Array()), system.Raw)
	}
	if got := system.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected system block type %q, got %q", "text", got)
	}
	if got := system.Get("0.text").String(); got != "You are a helpful assistant." {
		t.Fatalf("Expected system text %q, got %q", "You are a helpful assistant.", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}

func TestConvertOpenAIRequestToClaude_MultipleSystemMessagesMergedIntoTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "Rule 1"},
			{"role": "system", "content": [{"type": "text", "text": "Rule 2"}]},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("Expected 2 system blocks, got %d. System: %s", len(system), resultJSON.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "Rule 1" {
		t.Fatalf("Expected first system text %q, got %q", "Rule 1", got)
	}
	if got := system[1].Get("text").String(); got != "Rule 2" {
		t.Fatalf("Expected second system text %q, got %q", "Rule 2", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}
