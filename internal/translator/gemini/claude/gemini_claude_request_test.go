package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToGemini_ToolChoice_SpecificTool(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "hi"}
				]
			}
		],
		"tools": [
			{
				"name": "json",
				"description": "A JSON tool",
				"input_schema": {
					"type": "object",
					"properties": {}
				}
			}
		],
		"tool_choice": {"type": "tool", "name": "json"}
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	if got := gjson.GetBytes(output, "toolConfig.functionCallingConfig.mode").String(); got != "ANY" {
		t.Fatalf("Expected toolConfig.functionCallingConfig.mode 'ANY', got '%s'", got)
	}
	allowed := gjson.GetBytes(output, "toolConfig.functionCallingConfig.allowedFunctionNames").Array()
	if len(allowed) != 1 || allowed[0].String() != "json" {
		t.Fatalf("Expected allowedFunctionNames ['json'], got %s", gjson.GetBytes(output, "toolConfig.functionCallingConfig.allowedFunctionNames").Raw)
	}
}

func TestConvertClaudeRequestToGemini_ImageContent(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "describe this image"},
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "aGVsbG8="
						}
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}
	if got := parts[0].Get("text").String(); got != "describe this image" {
		t.Fatalf("Expected first part text 'describe this image', got '%s'", got)
	}
	if got := parts[1].Get("inline_data.mime_type").String(); got != "image/png" {
		t.Fatalf("Expected image mime type 'image/png', got '%s'", got)
	}
	if got := parts[1].Get("inline_data.data").String(); got != "aGVsbG8=" {
		t.Fatalf("Expected image data 'aGVsbG8=', got '%s'", got)
	}
}

func TestConvertClaudeRequestToGemini_StripsClaudeCodeAttribution(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=12345;"},
			{"type": "text", "text": "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
			{"type": "text", "text": "User system prompt"}
		],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	parts := gjson.GetBytes(output, "system_instruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 system parts after attribution strip, got %d: %s", len(parts), gjson.GetBytes(output, "system_instruction.parts").Raw)
	}
	if got := parts[0].Get("text").String(); got != "You are a Claude agent, built on Anthropic's Claude Agent SDK." {
		t.Fatalf("Unexpected first system part: %q", got)
	}
	if got := parts[1].Get("text").String(); got != "User system prompt" {
		t.Fatalf("Unexpected second system part: %q", got)
	}
	if gjson.GetBytes(output, `system_instruction.parts.#(text%"x-anthropic-billing-header:*")`).Exists() {
		t.Fatalf("Claude Code attribution block was forwarded: %s", gjson.GetBytes(output, "system_instruction.parts").Raw)
	}
}

func TestConvertClaudeRequestToGemini_ConvertsMessageSystemRoleToUserContent(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"system": [{"type": "text", "text": "Top-level rules"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]},
			{"role": "system", "content": "String mid-conversation rule"},
			{"role": "system", "content": [{"type": "text", "text": "Array mid-conversation rule"}]}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	if systemContent := gjson.GetBytes(output, `contents.#(role=="system")`); systemContent.Exists() {
		t.Fatalf("system role should not be emitted in contents: %s", systemContent.Raw)
	}

	contents := gjson.GetBytes(output, "contents").Array()
	if len(contents) != 3 {
		t.Fatalf("Expected the user and message-level system turns in contents, got %d: %s", len(contents), gjson.GetBytes(output, "contents").Raw)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected first content role user, got %q", got)
	}
	if got := contents[1].Get("role").String(); got != "user" {
		t.Fatalf("Expected message-level string system content to be downgraded to user role, got %q", got)
	}
	if got := contents[1].Get("parts.0.text").String(); got != "String mid-conversation rule" {
		t.Fatalf("Unexpected string message-level system content text: %q", got)
	}
	if got := contents[2].Get("role").String(); got != "user" {
		t.Fatalf("Expected message-level array system content to be downgraded to user role, got %q", got)
	}
	if got := contents[2].Get("parts.0.text").String(); got != "Array mid-conversation rule" {
		t.Fatalf("Unexpected array message-level system content text: %q", got)
	}

	parts := gjson.GetBytes(output, "system_instruction.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected only top-level system parts, got %d: %s", len(parts), gjson.GetBytes(output, "system_instruction.parts").Raw)
	}
	if got := parts[0].Get("text").String(); got != "Top-level rules" {
		t.Fatalf("Unexpected first system part: %q", got)
	}
}

func TestConvertClaudeRequestToGemini_SkipsEmptyTextParts(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": ""},
					{"type": "text", "text": "hello"},
					{"type": "text", "text": ""}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	parts := gjson.GetBytes(output, "contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part after skipping empty text, got %d: %s", len(parts), output)
	}
	if got := parts[0].Get("text").String(); got != "hello" {
		t.Fatalf("Expected part text 'hello', got '%s'", got)
	}
}

func TestConvertClaudeRequestToGemini_StructuredToolResult(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "json-call-1", "name": "json", "input": {"ok": true}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "json-call-1",
						"content": [
							{"type": "text", "text": "alpha"},
							{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}}
						]
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	fr := gjson.GetBytes(output, "contents.1.parts.0.functionResponse")
	if !fr.Exists() {
		t.Fatalf("expected functionResponse part, contents=%s", gjson.GetBytes(output, "contents").Raw)
	}
	// The text block must remain structured JSON, not a double-encoded string blob.
	if got := fr.Get("response.result.text").String(); got != "alpha" {
		t.Fatalf("expected structured result text 'alpha', got result=%s", fr.Get("response.result").Raw)
	}
	// The image block must be emitted as a separate inline_data part, not embedded in result.
	img := gjson.GetBytes(output, "contents.1.parts.1.inline_data")
	if got := img.Get("mime_type").String(); got != "image/png" {
		t.Fatalf("expected image mime type 'image/png', got '%s'", got)
	}
	if got := img.Get("data").String(); got != "aGVsbG8=" {
		t.Fatalf("expected image data 'aGVsbG8=', got '%s'", got)
	}
}

func TestConvertClaudeRequestToGemini_StringToolResult(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-flash-preview",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "json-call-1", "name": "json", "input": {"ok": true}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "json-call-1", "content": "alpha"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToGemini("gemini-3-flash-preview", inputJSON, false)

	fr := gjson.GetBytes(output, "contents.1.parts.0.functionResponse")
	if !fr.Exists() {
		t.Fatalf("expected functionResponse part, contents=%s", gjson.GetBytes(output, "contents").Raw)
	}
	// String content must not be double-encoded: result should be exactly "alpha".
	if got := fr.Get("response.result").String(); got != "alpha" {
		t.Fatalf("expected result 'alpha', got '%s' (raw=%s)", got, fr.Get("response.result").Raw)
	}
}
