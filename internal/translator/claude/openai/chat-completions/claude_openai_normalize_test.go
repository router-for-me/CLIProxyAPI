package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

// Cursor agent mode sends Anthropic-native tool_use / tool_result blocks and
// bare tool definitions to the OpenAI Chat Completions endpoint. These tests
// verify normalizeAnthropicRequestBlocks rewrites them into standard OpenAI
// shapes so the existing translation produces a valid Claude request.

func TestConvertOpenAIRequestToClaude_CursorToolResultBlock(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{"role": "user", "content": "list files"},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Let me check."},
					{"type": "tool_use", "id": "toolu_1", "name": "list_dir", "input": {"path": "."}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_1", "content": "a.txt\nb.txt"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	// assistant message must carry a tool_use block
	asst := messages[1]
	if got := asst.Get("role").String(); got != "assistant" {
		t.Fatalf("Expected messages[1].role assistant, got %q", got)
	}
	toolUse := asst.Get("content.#(type==tool_use)")
	if !toolUse.Exists() {
		t.Fatalf("Expected a tool_use block in assistant message, got: %s", asst.Raw)
	}
	if got := toolUse.Get("id").String(); got != "toolu_1" {
		t.Fatalf("Expected tool_use id toolu_1, got %q", got)
	}
	if got := toolUse.Get("name").String(); got != "list_dir" {
		t.Fatalf("Expected tool_use name list_dir, got %q", got)
	}
	if got := toolUse.Get("input.path").String(); got != "." {
		t.Fatalf("Expected tool_use input.path '.', got %q", got)
	}

	// the tool_result must become a user message with a tool_result block
	toolResultMsg := messages[2]
	if got := toolResultMsg.Get("role").String(); got != "user" {
		t.Fatalf("Expected messages[2].role user, got %q", got)
	}
	tr := toolResultMsg.Get("content.0")
	if got := tr.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result block, got %q (%s)", got, toolResultMsg.Raw)
	}
	if got := tr.Get("tool_use_id").String(); got != "toolu_1" {
		t.Fatalf("Expected tool_use_id toolu_1, got %q", got)
	}
	if got := tr.Get("content").String(); got != "a.txt\nb.txt" {
		t.Fatalf("Expected tool_result content, got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_CursorToolResultWithText(t *testing.T) {
	// A user turn that mixes a tool_result with a follow-up text instruction.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "toolu_9", "content": "done"},
					{"type": "text", "text": "now summarize"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages (tool_result + user text), got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	if got := messages[0].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("Expected first message tool_result, got %q", got)
	}
	if got := messages[1].Get("content.0.text").String(); got != "now summarize" {
		t.Fatalf("Expected trailing user text 'now summarize', got %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_BareAnthropicTools(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"name": "read_file",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	tools := resultJSON.Get("tools").Array()

	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d: %s", len(tools), resultJSON.Get("tools").Raw)
	}
	tool := tools[0]
	if got := tool.Get("name").String(); got != "read_file" {
		t.Fatalf("Expected tool name read_file, got %q", got)
	}
	if got := tool.Get("description").String(); got != "Read a file" {
		t.Fatalf("Expected tool description, got %q", got)
	}
	if got := tool.Get("input_schema.properties.path.type").String(); got != "string" {
		t.Fatalf("Expected input_schema preserved, got: %s", tool.Raw)
	}
}

func TestConvertOpenAIRequestToClaude_StandardOpenAIUnchanged(t *testing.T) {
	// A normal OpenAI payload must pass through normalization untouched.
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "do_work", "arguments": "{\"a\":1}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "ok"}
		],
		"tools": [
			{"type": "function", "function": {"name": "do_work", "description": "d", "parameters": {"type": "object"}}}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("content.0.type").String(); got != "tool_use" {
		t.Fatalf("Expected assistant tool_use, got %q", got)
	}
	if got := messages[0].Get("content.0.id").String(); got != "call_1" {
		t.Fatalf("Expected tool_use id call_1, got %q", got)
	}
	if got := messages[1].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("Expected tool_result, got %q", got)
	}
	if got := resultJSON.Get("tools.0.name").String(); got != "do_work" {
		t.Fatalf("Expected tool do_work, got %q", got)
	}
}
