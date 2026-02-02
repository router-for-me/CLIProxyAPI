package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestInjectThinkingBlockForToolUse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "assistant with tool_use but no thinking - should inject thinking",
			input: `{
				"model": "kimi-k2.5",
				"messages": [
					{
						"role": "assistant",
						"content": [
							{"type": "text", "text": "Let me use a tool"},
							{"type": "tool_use", "id": "tool_1", "name": "test_tool", "input": {}}
						]
					}
				]
			}`,
			expected: "thinking",
		},
		{
			name: "assistant with tool_use and thinking - should not modify",
			input: `{
				"model": "kimi-k2.5",
				"messages": [
					{
						"role": "assistant",
						"content": [
							{"type": "thinking", "thinking": "I need to use a tool"},
							{"type": "tool_use", "id": "tool_1", "name": "test_tool", "input": {}}
						]
					}
				]
			}`,
			expected: "thinking",
		},
		{
			name: "user message with tool_use - should not modify",
			input: `{
				"model": "kimi-k2.5",
				"messages": [
					{
						"role": "user",
						"content": [
							{"type": "tool_result", "tool_use_id": "tool_1", "content": "result"}
						]
					}
				]
			}`,
			expected: "",
		},
		{
			name: "assistant without tool_use - should not modify",
			input: `{
				"model": "kimi-k2.5",
				"messages": [
					{
						"role": "assistant",
						"content": [
							{"type": "text", "text": "Hello!"}
						]
					}
				]
			}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := injectThinkingBlockForToolUse([]byte(tt.input))
			
			// Check if thinking block exists in assistant messages with tool_use
			messages := gjson.GetBytes(result, "messages")
			if !messages.IsArray() {
				t.Fatal("messages is not an array")
			}

			for _, msg := range messages.Array() {
				if msg.Get("role").String() == "assistant" {
					content := msg.Get("content")
					if !content.IsArray() {
						continue
					}

					hasToolUse := false
					hasThinking := false
					for _, part := range content.Array() {
						partType := part.Get("type").String()
						if partType == "tool_use" {
							hasToolUse = true
						}
						if partType == "thinking" {
							hasThinking = true
						}
					}

					if hasToolUse && tt.expected == "thinking" && !hasThinking {
						t.Errorf("Expected thinking block in assistant message with tool_use, but not found")
					}
				}
			}
		})
	}
}

func TestApplyCompatibleClaude(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		config         thinking.ThinkingConfig
		expectThinking bool
	}{
		{
			name: "thinking enabled with tool_use - should inject thinking block",
			input: `{
				"model": "kimi-k2.5",
				"messages": [
					{
						"role": "assistant",
						"content": [
							{"type": "tool_use", "id": "tool_1", "name": "test_tool", "input": {}}
						]
					}
				]
			}`,
			config: thinking.ThinkingConfig{
				Mode:   thinking.ModeBudget,
				Budget: 4000,
			},
			expectThinking: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := applyCompatibleClaude([]byte(tt.input), tt.config)
			if err != nil {
				t.Fatalf("applyCompatibleClaude failed: %v", err)
			}

			// Check if thinking.type is enabled
			thinkingType := gjson.GetBytes(result, "thinking.type").String()
			if thinkingType != "enabled" {
				t.Errorf("Expected thinking.type=enabled, got %s", thinkingType)
			}

			// Check if thinking block is injected
			messages := gjson.GetBytes(result, "messages")
			if !messages.IsArray() {
				t.Fatal("messages is not an array")
			}

			for _, msg := range messages.Array() {
				if msg.Get("role").String() == "assistant" {
					content := msg.Get("content")
					if !content.IsArray() {
						continue
					}

					hasThinking := false
					for _, part := range content.Array() {
						if part.Get("type").String() == "thinking" {
							hasThinking = true
							break
						}
					}

					if tt.expectThinking && !hasThinking {
						t.Errorf("Expected thinking block in assistant message, but not found. Result: %s", string(result))
					}
				}
			}
		})
	}
}
