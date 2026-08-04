package claude

import (
	"encoding/base64"
	"fmt"
	"strings"
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
			name: "AC1: unsigned assistant thinking is mapped to reasoning_content",
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
			name: "AC3: unsigned thinking-only message maps to reasoning_content",
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
			name: "AC5: thinking in system role must be ignored",
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
			name: "Unsigned thinking parts are mapped to reasoning_content",
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
			name:                    "Unsigned thinking keeps reasoning_content for OpenAI-compatible models",
			signature:               "",
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

// TestConvertClaudeRequestToOpenAI_UnsignedThinkingPreserved verifies
// that unsigned thinking is preserved as reasoning_content for OpenAI-compatible models like DeepSeek.
func TestConvertClaudeRequestToOpenAI_UnsignedThinkingPreserved(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "What is 2+2?"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me calculate: 2+2=4"},
					{"type": "text", "text": "2+2 is 4"}
				]
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
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages, got %d. Messages: %v", len(messages), resultJSON.Get("messages").Raw)
	}

	assistantMsg := messages[1]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("Expected assistant message at index 1, got role %q", assistantMsg.Get("role").String())
	}

	if got := assistantMsg.Get("reasoning_content").String(); got != "Let me calculate: 2+2=4" {
		t.Fatalf("reasoning_content = %q, want %q. Output: %s", got, "Let me calculate: 2+2=4", string(result))
	}
}

// TestConvertClaudeRequestToOpenAI_MultiTurnWithToolCallsAndThinking verifies
// that multi-turn history containing thinking and tool_use preserves reasoning_content alongside tool_calls.
func TestConvertClaudeRequestToOpenAI_MultiTurnWithToolCallsAndThinking(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-7-sonnet",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Read file main.go"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "I need to call the read file tool to see main.go"},
					{"type": "tool_use", "id": "call_123", "name": "read_file", "input": {"path": "main.go"}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_123", "content": "package main\nfunc main() {}"}
				]
			}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 3 {
		t.Fatalf("Expected 3 messages (user, assistant with tool_calls + reasoning_content, tool response), got %d. Output: %s", len(messages), string(result))
	}

	assistantMsg := messages[1]
	if assistantMsg.Get("role").String() != "assistant" {
		t.Fatalf("Expected assistant message at index 1, got role %q", assistantMsg.Get("role").String())
	}

	if got := assistantMsg.Get("reasoning_content").String(); got != "I need to call the read file tool to see main.go" {
		t.Fatalf("reasoning_content = %q, want expected thinking string. Output: %s", got, string(result))
	}

	toolCalls := assistantMsg.Get("tool_calls").Array()
	if len(toolCalls) != 1 {
		t.Fatalf("Expected 1 tool_call, got %d. Output: %s", len(toolCalls), string(result))
	}
	if gotID := toolCalls[0].Get("id").String(); gotID != "call_123" {
		t.Fatalf("tool_call id = %q, want call_123", gotID)
	}
}

// TestConvertClaudeRequestToOpenAI_ReasoningContentPreservedAfterToolCall models a
// turn-2 request: history already contains an assistant message with an unsigned thinking
// block + a tool_use, followed by the tool result and a new user message. This is the
// exact request shape DeepSeek rejects with 400 if reasoning_content is missing from the
// assistant+tool_calls message:
//
//	"The `reasoning_content` in the thinking mode must be passed back to the API."
func TestConvertClaudeRequestToOpenAI_ReasoningContentPreservedAfterToolCall(t *testing.T) {
	inputJSON := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Read file"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"I should read the file"},
				{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"package main"}
			]},
			{"role":"user","content":[{"type":"text","text":"Now explain it"}]}
		]
	}`

	result := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSON), false)

	messages := gjson.ParseBytes(result).Get("messages").Array()

	var assistantMsg gjson.Result
	found := false
	for _, m := range messages {
		if m.Get("role").String() == "assistant" && m.Get("tool_calls").Exists() {
			assistantMsg = m
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no assistant message with tool_calls found in translated output")
	}
	if !assistantMsg.Get("reasoning_content").Exists() {
		t.Fatalf("reasoning_content missing from assistant message with tool_calls — DeepSeek would return 400. Output: %s", string(result))
	}
	if got := strings.TrimSpace(assistantMsg.Get("reasoning_content").String()); got == "" {
		t.Fatalf("reasoning_content is empty — DeepSeek requires a non-empty value. Output: %s", string(result))
	}
}

func TestConvertClaudeRequestToOpenAI_FallbackReasoningContentWhenThinkingUnavailable(t *testing.T) {
	// Case 1: Assistant message with tool_use but NO thinking block
	inputJSONNoThinking := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Read file"}]},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"package main"}
			]}
		]
	}`

	resultNoThinking := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSONNoThinking), false)
	assistantMsgNoThinking := gjson.GetBytes(resultNoThinking, "messages.1")
	if got := assistantMsgNoThinking.Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("expected fallback reasoning_content '[reasoning unavailable]' when thinking block is missing, got %q. Output: %s", got, string(resultNoThinking))
	}

	// Case 2: Assistant message with tool_use and a Claude-signed thinking block (filtered out)
	inputJSONClaudeSig := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Read file"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"Claude thought","signature":"claude#EjQ="},
				{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"package main"}
			]}
		]
	}`

	resultClaudeSig := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSONClaudeSig), false)
	assistantMsgClaudeSig := gjson.GetBytes(resultClaudeSig, "messages.1")
	if got := assistantMsgClaudeSig.Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("expected fallback reasoning_content '[reasoning unavailable]' when thinking block has Claude signature, got %q. Output: %s", got, string(resultClaudeSig))
	}

	// Negative Assertion 1: Assistant message without tool_calls and without thinking must NOT have reasoning_content injected
	inputJSONTextOnly := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Hello"}]},
			{"role":"assistant","content":[{"type":"text","text":"Hi there"}]}
		]
	}`
	resultTextOnly := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSONTextOnly), false)
	assistantMsgTextOnly := gjson.GetBytes(resultTextOnly, "messages.1")
	if assistantMsgTextOnly.Get("reasoning_content").Exists() {
		t.Fatalf("assistant message without tool_calls or thinking must NOT contain reasoning_content. Output: %s", string(resultTextOnly))
	}

	// Negative Assertion 2: Assistant message with tool_calls AND valid thinking must keep original thinking (not replaced by sentinel)
	inputJSONValidThinking := `{
		"model": "deepseek-v4-flash",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"Read file"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"Valid reasoning text"},
				{"type":"tool_use","id":"call_1","name":"read_file","input":{"path":"main.go"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":"package main"}
			]}
		]
	}`
	resultValidThinking := ConvertClaudeRequestToOpenAI("deepseek-v4-flash", []byte(inputJSONValidThinking), false)
	assistantMsgValidThinking := gjson.GetBytes(resultValidThinking, "messages.1")
	if got := assistantMsgValidThinking.Get("reasoning_content").String(); got != "Valid reasoning text" {
		t.Fatalf("expected original reasoning text 'Valid reasoning text', got %q. Output: %s", got, string(resultValidThinking))
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

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolContent := messages[1].Get("content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected first tool content type %q, got %q", "text", got)
	}
	if got := toolContent.Get("0.text").String(); got != "tool ok" {
		t.Fatalf("Expected first tool content text %q, got %q", "tool ok", got)
	}
	if got := toolContent.Get("1.type").String(); got != "image_url" {
		t.Fatalf("Expected second tool content type %q, got %q", "image_url", got)
	}
	if got := toolContent.Get("1.image_url.url").String(); got != "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==" {
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

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolContent := messages[1].Get("content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "image_url" {
		t.Fatalf("Expected tool content type %q, got %q", "image_url", got)
	}
	if got := toolContent.Get("0.image_url.url").String(); got != "https://example.com/tool.png" {
		t.Fatalf("Unexpected image_url: %q", got)
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

	if got := assistantMsg.Get("reasoning_content").String(); got != "t1\n\nt2" {
		t.Fatalf("reasoning_content = %q, want %q", got, "t1\n\nt2")
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
