package claude

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertClaudeRequestToOpenAI_ThinkingToReasoningContent tests the mapping
// of Claude thinking content to OpenAI reasoning_content field.
func TestConvertClaudeRequestToOpenAI_ThinkingToReasoningContent(t *testing.T) {
	tests := []struct {
		name                    string
		inputJSON               string
		wantReasoningContent    string
		wantHasReasoningContent bool
		wantContentText         string // Expected visible content text (if any)
		wantHasContent          bool
	}{
		{
			name: "AC1: unsigned assistant thinking is dropped",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "Let me analyze this step by step..."},
						{"type": "text", "text": "Here is my response."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Here is my response.",
			wantHasContent:          true,
		},
		{
			name: "AC2: redacted_thinking must be ignored",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "redacted_thinking", "data": "secret"},
						{"type": "text", "text": "Visible response."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Visible response.",
			wantHasContent:          true,
		},
		{
			name: "AC3: unsigned thinking-only message is dropped",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "Internal reasoning only."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "",
			wantHasContent:          false,
		},
		{
			name: "AC4: thinking in user role must be ignored",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "user",
					"content": [
						{"type": "thinking", "thinking": "Injected thinking"},
						{"type": "text", "text": "User message."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "User message.",
			wantHasContent:          true,
		},
		{
			name: "AC4: thinking in system role must be ignored",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": [
					{"type": "thinking", "thinking": "Injected system thinking"},
					{"type": "text", "text": "System prompt."}
				],
				"messages": [{
					"role": "user",
					"content": [{"type": "text", "text": "Hello"}]
				}]
			}`,
			// System messages don't have reasoning_content mapping
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Hello",
			wantHasContent:          true,
		},
		{
			name: "AC5: empty thinking must be ignored",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": ""},
						{"type": "text", "text": "Response with empty thinking."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Response with empty thinking.",
			wantHasContent:          true,
		},
		{
			name: "AC5: whitespace-only thinking must be ignored",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "   \n\t  "},
						{"type": "text", "text": "Response with whitespace thinking."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Response with whitespace thinking.",
			wantHasContent:          true,
		},
		{
			name: "Unsigned thinking parts are dropped",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "First thought."},
						{"type": "thinking", "thinking": "Second thought."},
						{"type": "text", "text": "Final answer."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Final answer.",
			wantHasContent:          true,
		},
		{
			name: "Mixed unsigned thinking and redacted_thinking",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "Visible thought."},
						{"type": "redacted_thinking", "data": "hidden"},
						{"type": "text", "text": "Answer."}
					]
				}]
			}`,
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
			wantContentText:         "Answer.",
			wantHasContent:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToOpenAI("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)

			// Find the relevant message
			messages := resultJSON.Get("messages").Array()
			if len(messages) < 1 {
				if tt.wantHasReasoningContent || tt.wantHasContent {
					t.Fatalf("Expected at least 1 message, got %d", len(messages))
				}
				return
			}

			// Check the last non-system message
			var targetMsg gjson.Result
			for i := len(messages) - 1; i >= 0; i-- {
				if messages[i].Get("role").String() != "system" {
					targetMsg = messages[i]
					break
				}
			}

			// Check reasoning_content
			gotReasoningContent := targetMsg.Get("reasoning_content").String()
			gotHasReasoningContent := targetMsg.Get("reasoning_content").Exists()

			if gotHasReasoningContent != tt.wantHasReasoningContent {
				t.Errorf("reasoning_content existence = %v, want %v", gotHasReasoningContent, tt.wantHasReasoningContent)
			}

			if gotReasoningContent != tt.wantReasoningContent {
				t.Errorf("reasoning_content = %q, want %q", gotReasoningContent, tt.wantReasoningContent)
			}

			// Check content
			content := targetMsg.Get("content")
			// content has meaningful content if it's a non-empty array, or a non-empty string
			var gotHasContent bool
			switch {
			case content.IsArray():
				gotHasContent = len(content.Array()) > 0
			case content.Type == gjson.String:
				gotHasContent = content.String() != ""
			default:
				gotHasContent = false
			}

			if gotHasContent != tt.wantHasContent {
				t.Errorf("content existence = %v, want %v", gotHasContent, tt.wantHasContent)
			}

			if tt.wantHasContent && tt.wantContentText != "" {
				// Find text content
				var foundText string
				content.ForEach(func(_, v gjson.Result) bool {
					if v.Get("type").String() == "text" {
						foundText = v.Get("text").String()
						return false
					}
					return true
				})
				if foundText != tt.wantContentText {
					t.Errorf("content text = %q, want %q", foundText, tt.wantContentText)
				}
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAI_SignedThinkingCompatibility(t *testing.T) {
	tests := []struct {
		name                    string
		signature               string
		wantReasoningContent    string
		wantHasReasoningContent bool
	}{
		{
			name:                    "GPT-compatible signature keeps reasoning_content",
			signature:               validGPTChatReasoningSignature(),
			wantReasoningContent:    "provider state",
			wantHasReasoningContent: true,
		},
		{
			name:                    "Claude signature drops reasoning_content",
			signature:               "claude#EjQ=",
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
		},
		{
			name:                    "Gemini signature drops reasoning_content",
			signature:               "gemini#EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA",
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
		},
		{
			name:                    "Unknown signature drops reasoning_content",
			signature:               "not-a-provider-signature",
			wantReasoningContent:    "",
			wantHasReasoningContent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "provider state", "signature": "` + tt.signature + `"},
						{"type": "text", "text": "visible answer"}
					]
				}]
			}`

			result := ConvertClaudeRequestToOpenAI("gpt-5", []byte(inputJSON), false)
			assistantMsg := gjson.GetBytes(result, "messages.0")
			gotReasoningContent := assistantMsg.Get("reasoning_content").String()
			gotHasReasoningContent := assistantMsg.Get("reasoning_content").Exists()

			if gotHasReasoningContent != tt.wantHasReasoningContent {
				t.Fatalf("reasoning_content exists = %v, want %v. Output: %s", gotHasReasoningContent, tt.wantHasReasoningContent, string(result))
			}
			if gotReasoningContent != tt.wantReasoningContent {
				t.Fatalf("reasoning_content = %q, want %q. Output: %s", gotReasoningContent, tt.wantReasoningContent, string(result))
			}
			if got := assistantMsg.Get("content.0.text").String(); got != "visible answer" {
				t.Fatalf("visible content = %q, want visible answer. Output: %s", got, string(result))
			}
		})
	}
}

// TestConvertClaudeRequestToOpenAI_UnsignedThinkingOnlyMessageDropped verifies
// that unsigned Claude thinking is not migrated into GPT reasoning state.
func TestConvertClaudeRequestToOpenAI_UnsignedThinkingOnlyMessageDropped(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "What is 2+2?"}]
			},
			{
				"role": "assistant",
				"content": [{"type": "thinking", "thinking": "Let me calculate: 2+2=4"}]
			},
			{
				"role": "user",
				"content": [{"type": "text", "text": "Thanks"}]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected unsigned thinking-only assistant message to be dropped, got %d. Messages: %v", len(messages), resultJSON.Get("messages").Raw)
	}
	for _, message := range messages {
		if message.Get("reasoning_content").Exists() {
			t.Fatalf("unsigned thinking should not produce reasoning_content. Messages: %v", resultJSON.Get("messages").Raw)
		}
	}
}

func validGPTChatReasoningSignature() string {
	raw := make([]byte, 1+8+16+16+32)
	raw[0] = 0x80
	raw[8] = 1
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	return base64.URLEncoding.EncodeToString(raw)
}

func TestConvertClaudeRequestToOpenAI_MessageSystemRoleWrapsAsUserReminder(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-5",
		"system": [{"type": "text", "text": "Top-level rules"}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hello"}]},
			{"role": "system", "content": "String mid-conversation rule"},
			{"role": "assistant", "content": [{"type": "text", "text": "Hi there"}]},
			{"role": "system", "content": [{"type": "text", "text": "Array mid-conversation rule"}]},
			{"role": "user", "content": [{"type": "text", "text": "Follow up"}]}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("gpt-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 6 {
		t.Fatalf("Expected 6 messages, got %d: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Get("role").String())
	}
	if got, want := roles, []string{"system", "user", "user", "assistant", "user", "user"}; fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
		t.Fatalf("Unexpected message roles: got %v, want %v", got, want)
	}

	systemContent := messages[0].Get("content").Array()
	if len(systemContent) != 1 {
		t.Fatalf("Expected only top-level system content, got %d items: %s", len(systemContent), messages[0].Get("content").Raw)
	}
	if got := systemContent[0].Get("text").String(); got != "Top-level rules" {
		t.Fatalf("system content = %q, want Top-level rules", got)
	}
	if got := messages[2].Get("content.0.text").String(); got != "<system-reminder>\nString mid-conversation rule\n</system-reminder>" {
		t.Fatalf("unexpected string reminder text: %q", got)
	}
	if got := messages[4].Get("content.0.text").String(); got != "<system-reminder>\nArray mid-conversation rule\n</system-reminder>" {
		t.Fatalf("unexpected array reminder text: %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_PreservesToolAdjacencyWithInterveningSystemMessage(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Execute tools"}]},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "tool_one", "input": {"a": 1}},
					{"type": "tool_use", "id": "call_2", "name": "tool_two", "input": {"b": 2}}
				]
			},
			{"role": "system", "content": "Context update between tool call and tool result"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_2", "content": "result 2"},
					{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"},
					{"type": "text", "text": "Now summarize"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("gpt-5", []byte(inputJSON), false)
	messages := gjson.GetBytes(result, "messages").Array()

	// Expected roles order:
	// 0: user ("Execute tools")
	// 1: assistant (tool_calls [call_1, call_2])
	// 2: tool (tool_call_id: call_1)
	// 3: tool (tool_call_id: call_2)
	// 4: user (<system-reminder>...)
	// 5: user ("Now summarize")
	roles := make([]string, 0, len(messages))
	for _, msg := range messages {
		roles = append(roles, msg.Get("role").String())
	}
	wantRoles := []string{"user", "assistant", "tool", "tool", "user", "user"}
	if fmt.Sprintf("%v", roles) != fmt.Sprintf("%v", wantRoles) {
		t.Fatalf("unexpected roles: got %v, want %v", roles, wantRoles)
	}

	// Verify tool messages immediately follow assistant
	if messages[2].Get("tool_call_id").String() != "call_1" {
		t.Fatalf("expected tool message 0 to respond to call_1, got %q", messages[2].Get("tool_call_id").String())
	}
	if messages[3].Get("tool_call_id").String() != "call_2" {
		t.Fatalf("expected tool message 1 to respond to call_2, got %q", messages[3].Get("tool_call_id").String())
	}
	if messages[4].Get("content.0.text").String() != "<system-reminder>\nContext update between tool call and tool result\n</system-reminder>" {
		t.Fatalf("unexpected system reminder content: %q", messages[4].Get("content.0.text").String())
	}
	if messages[5].Get("content.0.text").String() != "Now summarize" {
		t.Fatalf("unexpected user summary content: %q", messages[5].Get("content.0.text").String())
	}
}

func TestConvertClaudeRequestToOpenAI_SystemMessageScenarios(t *testing.T) {
	tests := []struct {
		name        string
		inputJSON   string
		wantHasSys  bool
		wantSysText string
	}{
		{
			name: "No system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasSys: false,
		},
		{
			name: "Empty string system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasSys: false,
		},
		{
			name: "String system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "Be helpful",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasSys:  true,
			wantSysText: "Be helpful",
		},
		{
			name: "Array system field with text",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": [{"type": "text", "text": "Array system"}],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasSys:  true,
			wantSysText: "Array system",
		},
		{
			name: "Array system field with multiple text blocks",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": [
					{"type": "text", "text": "Block 1"},
					{"type": "text", "text": "Block 2"}
				],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasSys:  true,
			wantSysText: "Block 2", // We will update the test logic to check all blocks or specifically the second one
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToOpenAI("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)
			messages := resultJSON.Get("messages").Array()

			hasSys := false
			var sysMsg gjson.Result
			if len(messages) > 0 && messages[0].Get("role").String() == "system" {
				hasSys = true
				sysMsg = messages[0]
			}

			if hasSys != tt.wantHasSys {
				t.Errorf("got hasSystem = %v, want %v", hasSys, tt.wantHasSys)
			}

			if tt.wantHasSys {
				// Check content - it could be string or array in OpenAI
				content := sysMsg.Get("content")
				var gotText string
				if content.IsArray() {
					arr := content.Array()
					if len(arr) > 0 {
						// Get the last element's text for validation
						gotText = arr[len(arr)-1].Get("text").String()
					}
				} else {
					gotText = content.String()
				}

				if tt.wantSysText != "" && gotText != tt.wantSysText {
					t.Errorf("got system text = %q, want %q", gotText, tt.wantSysText)
				}
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAI_ToolSchemaAddsMissingObjectProperties(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-opus",
		"tools": [
			{
				"name": "empty_params",
				"description": "No args",
				"input_schema": {"type": "object"}
			},
			{
				"name": "nested_params",
				"description": "Nested args",
				"input_schema": {
					"type": "object",
					"properties": {
						"nested": {"type": "object"},
						"items": {
							"type": "array",
							"items": {"type": "object"}
						}
					}
				}
			}
		],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	output := ConvertClaudeRequestToOpenAI("test-model", inputJSON, false)
	outputJSON := gjson.ParseBytes(output)

	if got := outputJSON.Get("tools.0.function.parameters.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("root object properties missing or invalid: %s", outputJSON.Get("tools.0.function.parameters").Raw)
	}
	if got := outputJSON.Get("tools.1.function.parameters.properties.nested.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("nested object properties missing or invalid: %s", outputJSON.Get("tools.1.function.parameters").Raw)
	}
	if got := outputJSON.Get("tools.1.function.parameters.properties.items.items.properties"); !got.Exists() || !got.IsObject() {
		t.Fatalf("array item object properties missing or invalid: %s", outputJSON.Get("tools.1.function.parameters").Raw)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultOrderAndContent(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "before"},
					{"type": "tool_result", "tool_use_id": "call_1", "content": [{"type":"text","text":"tool ok"}]},
					{"type": "text", "text": "after"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// OpenAI requires: tool messages MUST immediately follow assistant(tool_calls).
	// Correct order: assistant(tool_calls) + tool(result) + user(before+after)
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	if messages[0].Get("role").String() != "assistant" || !messages[0].Get("tool_calls").Exists() {
		t.Fatalf("Expected messages[0] to be assistant tool_calls, got %s: %s", messages[0].Get("role").String(), messages[0].Raw)
	}

	// tool message MUST immediately follow assistant(tool_calls) per OpenAI spec
	if messages[1].Get("role").String() != "tool" {
		t.Fatalf("Expected messages[1] to be tool (must follow tool_calls), got %s", messages[1].Get("role").String())
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_1" {
		t.Fatalf("Expected tool_call_id %q, got %q", "call_1", got)
	}
	if got := messages[1].Get("content").String(); got != "tool ok" {
		t.Fatalf("Expected tool content %q, got %q", "tool ok", got)
	}

	// User message comes after tool message
	if messages[2].Get("role").String() != "user" {
		t.Fatalf("Expected messages[2] to be user, got %s", messages[2].Get("role").String())
	}
	// User message should contain both "before" and "after" text
	if got := messages[2].Get("content.0.text").String(); got != "before" {
		t.Fatalf("Expected user text[0] %q, got %q", "before", got)
	}
	if got := messages[2].Get("content.1.text").String(); got != "after" {
		t.Fatalf("Expected user text[1] %q, got %q", "after", got)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultObjectContent(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_1", "content": {"foo": "bar"}}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// assistant(tool_calls) + tool(result)
	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	if messages[1].Get("role").String() != "tool" {
		t.Fatalf("Expected messages[1] to be tool, got %s", messages[1].Get("role").String())
	}

	toolContent := messages[1].Get("content").String()
	parsed := gjson.Parse(toolContent)
	if parsed.Get("foo").String() != "bar" {
		t.Fatalf("Expected tool content JSON foo=bar, got %q", toolContent)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultTextAndImageContent(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_1",
						"content": [
							{"type": "text", "text": "tool ok"},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUg=="
								}
							}
						]
					}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	// The tool message keeps only the textual payload; OpenAI drops image parts there.
	if got := messages[1].Get("role").String(); got != "tool" {
		t.Fatalf("Expected second message role %q, got %q", "tool", got)
	}
	if messages[1].Get("content").IsArray() {
		t.Fatalf("Tool content must not be an array, got %s", messages[1].Get("content").Raw)
	}
	if got := messages[1].Get("content").String(); got != "tool ok" {
		t.Fatalf("Expected tool content %q, got %q", "tool ok", got)
	}

	// The image is relayed as a user message directly after the tool result.
	relay := messages[2]
	if got := relay.Get("role").String(); got != "user" {
		t.Fatalf("Expected relay message role %q, got %q", "user", got)
	}
	relayContent := relay.Get("content")
	if !relayContent.IsArray() {
		t.Fatalf("Expected relay content array, got %s", relayContent.Raw)
	}
	if got := relayContent.Get("0.text").String(); got != toolResultImageRelayNotice {
		t.Fatalf("Expected relay notice %q, got %q", toolResultImageRelayNotice, got)
	}
	if got := relayContent.Get("1.type").String(); got != "image_url" {
		t.Fatalf("Expected relay content type %q, got %q", "image_url", got)
	}
	if got := relayContent.Get("1.image_url.url").String(); got != "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("Unexpected image_url: %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultURLImageOnly(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_1",
						"content": {
							"type": "image",
							"source": {
								"type": "url",
								"url": "https://example.com/tool.png"
							}
						}
					}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	// An image-only tool_result still needs a non-empty text payload on the tool message.
	if got := messages[1].Get("content").String(); got != toolResultImagePlaceholder {
		t.Fatalf("Expected tool content %q, got %q", toolResultImagePlaceholder, got)
	}

	relayContent := messages[2].Get("content")
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("Expected relay message role %q, got %q", "user", got)
	}
	if got := relayContent.Get("1.type").String(); got != "image_url" {
		t.Fatalf("Expected relay content type %q, got %q", "image_url", got)
	}
	if got := relayContent.Get("1.image_url.url").String(); got != "https://example.com/tool.png" {
		t.Fatalf("Unexpected image_url: %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_ToolResultImageMergesIntoUserText(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "screenshot", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_1",
						"content": [
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "iVBORw0KGgoAAAANSUhEUg=="
								}
							}
						]
					},
					{"type": "text", "text": "What color?"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// The relayed image joins the user text instead of adding a second user turn.
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("Expected third message role %q, got %q", "user", got)
	}

	content := messages[2].Get("content")
	if got := len(content.Array()); got != 3 {
		t.Fatalf("Expected 3 user content parts, got %d: %s", got, content.Raw)
	}
	if got := content.Get("0.text").String(); got != toolResultImageRelayNotice {
		t.Fatalf("Expected relay notice %q, got %q", toolResultImageRelayNotice, got)
	}
	if got := content.Get("1.type").String(); got != "image_url" {
		t.Fatalf("Expected second part type %q, got %q", "image_url", got)
	}
	if got := content.Get("2.text").String(); got != "What color?" {
		t.Fatalf("Expected trailing user text %q, got %q", "What color?", got)
	}
}

func TestConvertClaudeRequestToOpenAI_MultipleToolResultsWithImages(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "shot1", "input": {}},
					{"type": "tool_use", "id": "call_2", "name": "shot2", "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_1",
						"content": [
							{"type": "text", "text": "result 1"},
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "img1"
								}
							}
						]
					},
					{
						"type": "tool_result",
						"tool_use_id": "call_2",
						"content": {
							"type": "image",
							"source": {
								"type": "url",
								"url": "https://example.com/2.png"
							}
						}
					}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// Expected: assistant(2 tool calls), tool(call_1), tool(call_2), user(relay 2 images)
	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[1].Get("role").String(); got != "tool" || messages[1].Get("tool_call_id").String() != "call_1" {
		t.Fatalf("Expected tool 1 message, got: %s", messages[1].Raw)
	}
	if got := messages[1].Get("content").String(); got != "result 1" {
		t.Fatalf("Expected tool 1 content 'result 1', got %q", got)
	}
	if got := messages[2].Get("role").String(); got != "tool" || messages[2].Get("tool_call_id").String() != "call_2" {
		t.Fatalf("Expected tool 2 message, got: %s", messages[2].Raw)
	}
	if got := messages[2].Get("content").String(); got != toolResultImagePlaceholder {
		t.Fatalf("Expected tool 2 placeholder, got %q", got)
	}
	if got := messages[3].Get("role").String(); got != "user" {
		t.Fatalf("Expected user relay message, got: %s", messages[3].Raw)
	}
	relayContent := messages[3].Get("content").Array()
	if len(relayContent) != 3 {
		t.Fatalf("Expected 3 parts in relay (notice + 2 images), got %d: %s", len(relayContent), messages[3].Get("content").Raw)
	}
	if got := relayContent[0].Get("text").String(); got != toolResultImageRelayNotice {
		t.Fatalf("Expected notice %q, got %q", toolResultImageRelayNotice, got)
	}
	if got := relayContent[1].Get("image_url.url").String(); got != "data:image/png;base64,img1" {
		t.Fatalf("Expected image 1 url, got %q", got)
	}
	if got := relayContent[2].Get("image_url.url").String(); got != "https://example.com/2.png" {
		t.Fatalf("Expected image 2 url, got %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_AssistantTextToolUseTextOrder(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "pre"},
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}},
					{"type": "text", "text": "post"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// New behavior: content + tool_calls unified in single assistant message
	// Expect: assistant(content[pre,post] + tool_calls)
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	assistantMsg := messages[0]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("Expected messages[0] to be assistant, got %s", assistantMsg.Get("role").String())
	}

	// Should have both content and tool_calls in same message
	if !assistantMsg.Get("tool_calls").Exists() {
		t.Fatalf("Expected assistant message to have tool_calls")
	}
	if got := assistantMsg.Get("tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("Expected tool_call id %q, got %q", "call_1", got)
	}
	if got := assistantMsg.Get("tool_calls.0.function.name").String(); got != "do_work" {
		t.Fatalf("Expected tool_call name %q, got %q", "do_work", got)
	}

	// Content should have both pre and post text
	if got := assistantMsg.Get("content.0.text").String(); got != "pre" {
		t.Fatalf("Expected content[0] text %q, got %q", "pre", got)
	}
	if got := assistantMsg.Get("content.1.text").String(); got != "post" {
		t.Fatalf("Expected content[1] text %q, got %q", "post", got)
	}
}

func TestConvertClaudeRequestToOpenAI_AssistantThinkingToolUseThinkingSplit(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "t1"},
					{"type": "text", "text": "pre"},
					{"type": "tool_use", "id": "call_1", "name": "do_work", "input": {"a": 1}},
					{"type": "thinking", "thinking": "t2"},
					{"type": "text", "text": "post"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	// Unsigned thinking is dropped, while text and tool_calls remain unified.
	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	assistantMsg := messages[0]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("Expected messages[0] to be assistant, got %s", assistantMsg.Get("role").String())
	}

	// Should have content with both pre and post
	if got := assistantMsg.Get("content.0.text").String(); got != "pre" {
		t.Fatalf("Expected content[0] text %q, got %q", "pre", got)
	}
	if got := assistantMsg.Get("content.1.text").String(); got != "post" {
		t.Fatalf("Expected content[1] text %q, got %q", "post", got)
	}

	// Should have tool_calls
	if !assistantMsg.Get("tool_calls").Exists() {
		t.Fatalf("Expected assistant message to have tool_calls")
	}

	if assistantMsg.Get("reasoning_content").Exists() {
		t.Fatalf("unsigned thinking should not produce reasoning_content: %s", assistantMsg.Raw)
	}
}

func TestConvertClaudeRequestToOpenAI_StripsClaudeCodeAttribution(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"system": [
			{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=cli; cch=12345;"},
			{"type": "text", "text": "User system prompt"}
		],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "hi"}]}]
	}`)

	output := ConvertClaudeRequestToOpenAI("gpt-5", inputJSON, false)
	messages := gjson.GetBytes(output, "messages").Array()
	if len(messages) == 0 || messages[0].Get("role").String() != "system" {
		t.Fatalf("Expected first message to be system, got: %s", gjson.GetBytes(output, "messages").Raw)
	}

	content := messages[0].Get("content").Array()
	if len(content) != 1 {
		t.Fatalf("Expected 1 system content item after attribution strip, got %d: %s", len(content), messages[0].Get("content").Raw)
	}
	if got := content[0].Get("text").String(); got != "User system prompt" {
		t.Fatalf("Unexpected system content: %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_StopSequences(t *testing.T) {
	tests := []struct {
		name      string
		inputJSON string
		wantStop  []string
	}{
		{
			name: "single stop sequence is emitted as array",
			inputJSON: `{
				"model": "claude-3-opus",
				"stop_sequences": ["</block>"],
				"messages": [{"role": "user", "content": "hi"}]
			}`,
			wantStop: []string{"</block>"},
		},
		{
			name: "multiple stop sequences are emitted as array",
			inputJSON: `{
				"model": "claude-3-opus",
				"stop_sequences": ["stop1", "stop2"],
				"messages": [{"role": "user", "content": "hi"}]
			}`,
			wantStop: []string{"stop1", "stop2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := ConvertClaudeRequestToOpenAI("gpt-4o", []byte(tt.inputJSON), false)
			stopRes := gjson.GetBytes(output, "stop")
			if !stopRes.Exists() {
				t.Fatalf("expected 'stop' field in output, got: %s", string(output))
			}
			if !stopRes.IsArray() {
				t.Fatalf("expected 'stop' field to be JSON array, got: %s", stopRes.Raw)
			}
			items := stopRes.Array()
			if len(items) != len(tt.wantStop) {
				t.Fatalf("expected %d stop items, got %d (%v)", len(tt.wantStop), len(items), stopRes.Raw)
			}
			for i, want := range tt.wantStop {
				if items[i].String() != want {
					t.Errorf("stop[%d] = %q, want %q", i, items[i].String(), want)
				}
			}
		})
	}
}

func TestConvertClaudeRequestToOpenAI_ToolWithoutInputSchemaDefaultsParameters(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-opus-5",
		"tools": [
			{
				"type": "web_search_20250305",
				"name": "web_search",
				"max_uses": 8
			},
			{
				"name": "no_schema_custom"
			},
			{
				"name": "null_schema_custom",
				"input_schema": null
			}
		],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	output := ConvertClaudeRequestToOpenAI("test-model", inputJSON, false)
	outputJSON := gjson.ParseBytes(output)

	for i, toolName := range []string{"web_search", "no_schema_custom", "null_schema_custom"} {
		path := fmt.Sprintf("tools.%d.function", i)
		if got := outputJSON.Get(path + ".name").String(); got != toolName {
			t.Fatalf("tool %d name = %q, want %q", i, got, toolName)
		}
		params := outputJSON.Get(path + ".parameters")
		if !params.Exists() {
			t.Fatalf("tool %d function.parameters missing: %s", i, outputJSON.Get(path).Raw)
		}
		if got := params.Get("type").String(); got != "object" {
			t.Fatalf("tool %d function.parameters.type = %q, want object", i, got)
		}
		if got := params.Get("properties"); !got.Exists() || !got.IsObject() {
			t.Fatalf("tool %d function.parameters.properties missing or not object: %s", i, params.Raw)
		}
	}
}

func TestConvertClaudeRequestToOpenAI_StripsUnsupportedUnicodePropertyEscapePatterns(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.6",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{
			"name": "Artifact",
			"description": "Render an HTML file to an Artifact",
			"input_schema": {
				"type": "object",
				"properties": {
					"field": {
						"type": "string",
						"description": "field to replace",
						"pattern": "^(?!__.*__$)[^\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}\"\\\\./[\\]]{1,200}$"
					},
					"asset_id": {
						"type": "string",
						"pattern": "^[0-9a-f]{32}$"
					},
					"lookahead_safe": {
						"type": "string",
						"pattern": "^(?!__.*__$).{1,200}$"
					}
				}
			}
		}]
	}`)

	output := ConvertClaudeRequestToOpenAI("gpt-5.6", inputJSON, false)
	outputJSON := gjson.ParseBytes(output)

	params := outputJSON.Get("tools.0.function.parameters")
	if !params.Exists() {
		t.Fatalf("expected function.parameters in output: %s", output)
	}

	// Incompatible \p{...} pattern must be stripped
	if params.Get("properties.field.pattern").Exists() {
		t.Errorf("expected properties.field.pattern to be removed, got: %s", params.Get("properties.field.pattern").Raw)
	}
	if got := params.Get("properties.field.type").String(); got != "string" {
		t.Errorf("expected properties.field.type == 'string', got %q", got)
	}

	// Valid patterns must remain intact
	if got := params.Get("properties.asset_id.pattern").String(); got != "^[0-9a-f]{32}$" {
		t.Errorf("expected properties.asset_id.pattern preserved, got %q", got)
	}
	if got := params.Get("properties.lookahead_safe.pattern").String(); got != "^(?!__.*__$).{1,200}$" {
		t.Errorf("expected properties.lookahead_safe.pattern preserved, got %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_PreservesNonSchemaPatternKeys(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.6",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{
			"name": "config_tool",
			"input_schema": {
				"type": "object",
				"properties": {
					"regex_config": {
						"type": "object",
						"default": {
							"pattern": "\\p{L}+"
						},
						"enum": [
							{"pattern": "\\p{N}+"}
						]
					},
					"real_schema": {
						"type": "string",
						"pattern": "\\p{L}+"
					}
				}
			}
		}]
	}`)

	output := ConvertClaudeRequestToOpenAI("gpt-5.6", inputJSON, false)
	outputJSON := gjson.ParseBytes(output)

	params := outputJSON.Get("tools.0.function.parameters")
	if !params.Exists() {
		t.Fatalf("expected function.parameters in output: %s", output)
	}

	// Real schema pattern must be removed
	if params.Get("properties.real_schema.pattern").Exists() {
		t.Errorf("expected real_schema.pattern to be removed, got: %s", params.Get("properties.real_schema").Raw)
	}

	// User data under default and enum must be PRESERVED
	if got := params.Get("properties.regex_config.default.pattern").String(); got != `\p{L}+` {
		t.Errorf("expected default.pattern preserved, got %q", got)
	}
	if got := params.Get("properties.regex_config.enum.0.pattern").String(); got != `\p{N}+` {
		t.Errorf("expected enum.0.pattern preserved, got %q", got)
	}
}

func TestConvertClaudeRequestToOpenAI_StripsPatternPropertiesIncompatibleKeys(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.6",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{
			"name": "pattern_tool",
			"input_schema": {
				"type": "object",
				"patternProperties": {
					"^\\\\p{L}+$": {
						"type": "string"
					},
					"^[a-z]+$": {
						"type": "number"
					}
				}
			}
		}]
	}`)

	output := ConvertClaudeRequestToOpenAI("gpt-5.6", inputJSON, false)
	outputJSON := gjson.ParseBytes(output)

	params := outputJSON.Get("tools.0.function.parameters")
	if !params.Exists() {
		t.Fatalf("expected function.parameters in output: %s", output)
	}

	patternProps := params.Get("patternProperties").Map()
	if _, exists := patternProps[`^\p{L}+$`]; exists {
		t.Errorf("expected patternProperties key '^\\\\p{L}+$' to be removed, got: %s", params.Get("patternProperties").Raw)
	}
	if _, exists := patternProps[`^[a-z]+$`]; !exists {
		t.Errorf("expected patternProperties key '^[a-z]+$' to be preserved, got: %s", params.Get("patternProperties").Raw)
	}
}
