package claude

import (
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
			name: "AC1: assistant message with thinking and text",
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
			wantReasoningContent:    "Let me analyze this step by step...",
			wantHasReasoningContent: true,
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
			name: "AC3: thinking-only message preserved with reasoning_content",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{
					"role": "assistant",
					"content": [
						{"type": "thinking", "thinking": "Internal reasoning only."}
					]
				}]
			}`,
			wantReasoningContent:    "Internal reasoning only.",
			wantHasReasoningContent: true,
			wantContentText:         "",
			// For OpenAI compatibility, content field is set to empty string "" when no text content exists
			wantHasContent: false,
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
			name: "Multiple thinking parts concatenated",
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
			wantReasoningContent:    "First thought.\n\nSecond thought.",
			wantHasReasoningContent: true,
			wantContentText:         "Final answer.",
			wantHasContent:          true,
		},
		{
			name: "Mixed thinking and redacted_thinking",
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
			wantReasoningContent:    "Visible thought.",
			wantHasReasoningContent: true,
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

// TestConvertClaudeRequestToOpenAI_ThinkingOnlyMessagePreserved tests AC3:
// that a message with only thinking content is preserved (not dropped).
func TestConvertClaudeRequestToOpenAI_ThinkingOnlyMessagePreserved(t *testing.T) {
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

	// Should have: user + assistant (thinking-only) + user = 3 messages
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %v", len(messages), resultJSON.Get("messages").Raw)
	}

	// Check the assistant message (index 1) has reasoning_content
	assistantMsg := messages[1]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Errorf("Expected message[1] to be assistant, got %s", assistantMsg.Get("role").String())
	}

	if !assistantMsg.Get("reasoning_content").Exists() {
		t.Error("Expected assistant message to have reasoning_content")
	}

	if assistantMsg.Get("reasoning_content").String() != "Let me calculate: 2+2=4" {
		t.Errorf("Unexpected reasoning_content: %s", assistantMsg.Get("reasoning_content").String())
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

	// New behavior: all content, thinking, and tool_calls unified in single assistant message
	// Expect: assistant(content[pre,post] + tool_calls + reasoning_content[t1+t2])
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

	// Should have combined reasoning_content from both thinking blocks
	if got := assistantMsg.Get("reasoning_content").String(); got != "t1\n\nt2" {
		t.Fatalf("Expected reasoning_content %q, got %q", "t1\n\nt2", got)
	}
}
