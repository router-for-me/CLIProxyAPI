package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

// TestAssistantMessageWithContentAndToolCalls verifies that assistant messages
// with both content (text) and tool_calls don't create duplicate content entries.
func TestAssistantMessageWithContentAndToolCalls(t *testing.T) {
	// OpenAI format request with assistant message containing both content and tool_calls
	// This simulates what LiteLLM sends after converting from Anthropic format
	input := []byte(`{
		"model": "gemini-claude-opus-4-5-thinking",
		"messages": [
			{"role": "user", "content": "Hello"},
			{
				"role": "assistant",
				"content": "Let me help you with that.",
				"tool_calls": [
					{
						"id": "call_123",
						"type": "function",
						"function": {
							"name": "search",
							"arguments": "{\"query\": \"test\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_123",
				"content": "Search result: found 10 items"
			},
			{"role": "user", "content": "Thanks!"}
		]
	}`)

	result := ConvertOpenAIRequestToAntigravity("gemini-claude-opus-4-5-thinking", input, false)

	// Parse the result
	contents := gjson.GetBytes(result, "request.contents")
	if !contents.IsArray() {
		t.Fatal("Expected request.contents to be an array")
	}

	contentsArr := contents.Array()
	t.Logf("Number of content entries: %d", len(contentsArr))

	// Count how many model role entries we have
	modelContentCount := 0
	for i, c := range contentsArr {
		role := c.Get("role").String()
		parts := c.Get("parts").Array()
		t.Logf("Content[%d]: role=%s, parts=%d", i, role, len(parts))

		if role == "model" {
			modelContentCount++
			// Log parts for debugging
			for j, p := range parts {
				if p.Get("text").Exists() {
					t.Logf("  Part[%d]: text=%q", j, p.Get("text").String()[:min(50, len(p.Get("text").String()))])
				}
				if p.Get("functionCall").Exists() {
					t.Logf("  Part[%d]: functionCall.name=%s", j, p.Get("functionCall.name").String())
				}
			}
		}
	}

	// The bug: with both content and tool_calls, the model content gets added twice
	// Expected: 1 model content entry (with both text and functionCall in parts)
	// Bug result: 2 model content entries (one with just text, one with text + functionCall)
	if modelContentCount != 1 {
		t.Errorf("Expected 1 model content entry, got %d (this indicates duplicate content bug)", modelContentCount)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
