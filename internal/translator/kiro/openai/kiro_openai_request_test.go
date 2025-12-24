package openai

import (
	"encoding/json"
	"testing"
)

// TestToolResultsAttachedToCurrentMessage verifies that tool results from "tool" role messages
// are properly attached to the current user message (the last message in the conversation).
// This is critical for LiteLLM-translated requests where tool results appear as separate messages.
func TestToolResultsAttachedToCurrentMessage(t *testing.T) {
	// OpenAI format request simulating LiteLLM's translation from Anthropic format
	// Sequence: user -> assistant (with tool_calls) -> tool (result) -> user
	// The last user message should have the tool results attached
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Hello, can you read a file for me?"},
			{
				"role": "assistant",
				"content": "I'll read that file for you.",
				"tool_calls": [
					{
						"id": "call_abc123",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/test.txt\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_abc123",
				"content": "File contents: Hello World!"
			},
			{"role": "user", "content": "What did the file say?"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// The last user message becomes currentMessage
	// History should have: user (first), assistant (with tool_calls)
	t.Logf("History count: %d", len(payload.ConversationState.History))
	if len(payload.ConversationState.History) != 2 {
		t.Errorf("Expected 2 history entries (user + assistant), got %d", len(payload.ConversationState.History))
	}

	// Tool results should be attached to currentMessage (the last user message)
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		t.Fatal("Expected currentMessage to have UserInputMessageContext with tool results")
	}

	if len(ctx.ToolResults) != 1 {
		t.Fatalf("Expected 1 tool result in currentMessage, got %d", len(ctx.ToolResults))
	}

	tr := ctx.ToolResults[0]
	if tr.ToolUseID != "call_abc123" {
		t.Errorf("Expected toolUseId 'call_abc123', got '%s'", tr.ToolUseID)
	}
	if len(tr.Content) == 0 || tr.Content[0].Text != "File contents: Hello World!" {
		t.Errorf("Tool result content mismatch, got: %+v", tr.Content)
	}
}

// TestToolResultsInHistoryUserMessage verifies that when there are multiple user messages
// after tool results, the tool results are attached to the correct user message in history.
func TestToolResultsInHistoryUserMessage(t *testing.T) {
	// Sequence: user -> assistant (with tool_calls) -> tool (result) -> user -> assistant -> user
	// The first user after tool should have tool results in history
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Hello"},
			{
				"role": "assistant",
				"content": "I'll read the file.",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "File result"
			},
			{"role": "user", "content": "Thanks for the file"},
			{"role": "assistant", "content": "You're welcome"},
			{"role": "user", "content": "Bye"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// History should have: user, assistant, user (with tool results), assistant
	// CurrentMessage should be: last user "Bye"
	t.Logf("History count: %d", len(payload.ConversationState.History))

	// Find the user message in history with tool results
	foundToolResults := false
	for i, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil {
			t.Logf("History[%d]: user message content=%q", i, h.UserInputMessage.Content)
			if h.UserInputMessage.UserInputMessageContext != nil {
				if len(h.UserInputMessage.UserInputMessageContext.ToolResults) > 0 {
					foundToolResults = true
					t.Logf("  Found %d tool results", len(h.UserInputMessage.UserInputMessageContext.ToolResults))
					tr := h.UserInputMessage.UserInputMessageContext.ToolResults[0]
					if tr.ToolUseID != "call_1" {
						t.Errorf("Expected toolUseId 'call_1', got '%s'", tr.ToolUseID)
					}
				}
			}
		}
		if h.AssistantResponseMessage != nil {
			t.Logf("History[%d]: assistant message content=%q", i, h.AssistantResponseMessage.Content)
		}
	}

	if !foundToolResults {
		t.Error("Tool results were not attached to any user message in history")
	}
}

// TestToolResultsWithMultipleToolCalls verifies handling of multiple tool calls
func TestToolResultsWithMultipleToolCalls(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Read two files for me"},
			{
				"role": "assistant",
				"content": "I'll read both files.",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/file1.txt\"}"
						}
					},
					{
						"id": "call_2",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/file2.txt\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "Content of file 1"
			},
			{
				"role": "tool",
				"tool_call_id": "call_2",
				"content": "Content of file 2"
			},
			{"role": "user", "content": "What do they say?"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	t.Logf("History count: %d", len(payload.ConversationState.History))
	t.Logf("CurrentMessage content: %q", payload.ConversationState.CurrentMessage.UserInputMessage.Content)

	// Check if there are any tool results anywhere
	var totalToolResults int
	for i, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil && h.UserInputMessage.UserInputMessageContext != nil {
			count := len(h.UserInputMessage.UserInputMessageContext.ToolResults)
			t.Logf("History[%d] user message has %d tool results", i, count)
			totalToolResults += count
		}
	}

	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil {
		t.Logf("CurrentMessage has %d tool results", len(ctx.ToolResults))
		totalToolResults += len(ctx.ToolResults)
	} else {
		t.Logf("CurrentMessage has no UserInputMessageContext")
	}

	if totalToolResults != 2 {
		t.Errorf("Expected 2 tool results total, got %d", totalToolResults)
	}
}

// TestToolResultsAtEndOfConversation verifies tool results are handled when
// the conversation ends with tool results (no following user message)
func TestToolResultsAtEndOfConversation(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Read a file"},
			{
				"role": "assistant",
				"content": "Reading the file.",
				"tool_calls": [
					{
						"id": "call_end",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/test.txt\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_end",
				"content": "File contents here"
			}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// When the last message is a tool result, a synthetic user message is created
	// and tool results should be attached to it
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil || len(ctx.ToolResults) == 0 {
		t.Error("Expected tool results to be attached to current message when conversation ends with tool result")
	} else {
		if ctx.ToolResults[0].ToolUseID != "call_end" {
			t.Errorf("Expected toolUseId 'call_end', got '%s'", ctx.ToolResults[0].ToolUseID)
		}
	}
}

// TestToolResultsFollowedByAssistant verifies handling when tool results are followed
// by an assistant message (no intermediate user message).
// This is the pattern from LiteLLM translation of Anthropic format where:
// user message has ONLY tool_result blocks -> LiteLLM creates tool messages
// then the next message is assistant
func TestToolResultsFollowedByAssistant(t *testing.T) {
	// Sequence: user -> assistant (with tool_calls) -> tool -> tool -> assistant -> user
	// This simulates LiteLLM's translation of:
	//   user: "Read files"
	//   assistant: [tool_use, tool_use]
	//   user: [tool_result, tool_result]  <- becomes multiple "tool" role messages
	//   assistant: "I've read them"
	//   user: "What did they say?"
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Read two files for me"},
			{
				"role": "assistant",
				"content": "I'll read both files.",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/a.txt\"}"
						}
					},
					{
						"id": "call_2",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"file_path\": \"/tmp/b.txt\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "Contents of file A"
			},
			{
				"role": "tool",
				"tool_call_id": "call_2",
				"content": "Contents of file B"
			},
			{
				"role": "assistant",
				"content": "I've read both files."
			},
			{"role": "user", "content": "What did they say?"}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	t.Logf("History count: %d", len(payload.ConversationState.History))

	// Tool results should be attached to a synthetic user message or the history should be valid
	var totalToolResults int
	for i, h := range payload.ConversationState.History {
		if h.UserInputMessage != nil {
			t.Logf("History[%d]: user message content=%q", i, h.UserInputMessage.Content)
			if h.UserInputMessage.UserInputMessageContext != nil {
				count := len(h.UserInputMessage.UserInputMessageContext.ToolResults)
				t.Logf("  Has %d tool results", count)
				totalToolResults += count
			}
		}
		if h.AssistantResponseMessage != nil {
			t.Logf("History[%d]: assistant message content=%q", i, h.AssistantResponseMessage.Content)
		}
	}

	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx != nil {
		t.Logf("CurrentMessage has %d tool results", len(ctx.ToolResults))
		totalToolResults += len(ctx.ToolResults)
	}

	if totalToolResults != 2 {
		t.Errorf("Expected 2 tool results total, got %d", totalToolResults)
	}
}

// TestAssistantEndsConversation verifies handling when assistant is the last message
func TestAssistantEndsConversation(t *testing.T) {
	input := []byte(`{
		"model": "kiro-claude-opus-4-5-agentic",
		"messages": [
			{"role": "user", "content": "Hello"},
			{
				"role": "assistant",
				"content": "Hi there!"
			}
		]
	}`)

	result, _ := BuildKiroPayloadFromOpenAI(input, "kiro-model", "", "CLI", false, false, nil, nil)

	var payload KiroPayload
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	// When assistant is last, a "Continue" user message should be created
	if payload.ConversationState.CurrentMessage.UserInputMessage.Content == "" {
		t.Error("Expected a 'Continue' message to be created when assistant is last")
	}
}
