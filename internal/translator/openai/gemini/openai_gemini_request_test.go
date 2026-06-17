package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToOpenAI_ToolCallIDMatching_MultipleCallsMultipleResponses(t *testing.T) {
	// Two function calls (Bash, Read) followed by two function responses.
	// Before the fix, both responses would reuse the last tool call ID (Read),
	// causing OpenAI-compatible upstreams to reject the request with
	// "Duplicate value for 'tool_call_id'".
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "Let me check."}
				]
			},
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}},
					{"functionCall": {"name": "Read", "args": {"path": "/a"}}},
					{"functionResponse": {"name": "Bash", "response": {"output": "file1.txt"}}},
					{"functionResponse": {"name": "Read", "response": {"content": "hello"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("deepseek-v4-pro", input, true)

	messages := gjson.GetBytes(out, "messages")
	if !messages.IsArray() {
		t.Fatal("expected messages to be an array")
	}

	// Collect tool message tool_call_ids
	var toolCallIDs []string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "tool" {
			id := msg.Get("tool_call_id").String()
			toolCallIDs = append(toolCallIDs, id)
		}
		return true
	})

	if len(toolCallIDs) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(toolCallIDs))
	}

	if toolCallIDs[0] == toolCallIDs[1] {
		t.Errorf("tool_call_ids must be distinct, got duplicate %q", toolCallIDs[0])
	}
}

func TestConvertGeminiRequestToOpenAI_ToolCallIDMatching_FIFOConsumption(t *testing.T) {
	// Three function calls, three function responses in order.
	// Verify that each response consumes the next pending tool call ID.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "A", "args": {}}},
					{"functionCall": {"name": "B", "args": {}}},
					{"functionCall": {"name": "C", "args": {}}},
					{"functionResponse": {"name": "A", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "B", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "C", "response": {"result": "ok"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("deepseek-v4-pro", input, false)

	messages := gjson.GetBytes(out, "messages")
	var toolCallIDs []string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "tool" {
			id := msg.Get("tool_call_id").String()
			toolCallIDs = append(toolCallIDs, id)
		}
		return true
	})

	if len(toolCallIDs) != 3 {
		t.Fatalf("expected 3 tool messages, got %d", len(toolCallIDs))
	}

	seen := make(map[string]bool)
	for _, id := range toolCallIDs {
		if seen[id] {
			t.Errorf("duplicate tool_call_id %q", id)
		}
		seen[id] = true
	}
}

func TestConvertGeminiRequestToOpenAI_ToolCallIDMatching_NoFunctionCalls(t *testing.T) {
	// functionResponse without any preceding functionCall should get a generated ID.
	input := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "Bash", "response": {"output": "done"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("deepseek-v4-pro", input, false)

	messages := gjson.GetBytes(out, "messages")
	var toolCallIDs []string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "tool" {
			id := msg.Get("tool_call_id").String()
			toolCallIDs = append(toolCallIDs, id)
		}
		return true
	})

	if len(toolCallIDs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(toolCallIDs))
	}
	if toolCallIDs[0] == "" {
		t.Error("tool_call_id should not be empty")
	}
}

func TestConvertGeminiRequestToOpenAI_ToolCallIDMatching_MoreResponsesThanCalls(t *testing.T) {
	// Two function calls, three function responses.
	// The extra response should get a generated ID.
	input := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "A", "args": {}}},
					{"functionCall": {"name": "B", "args": {}}},
					{"functionResponse": {"name": "A", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "B", "response": {"result": "ok"}}},
					{"functionResponse": {"name": "C", "response": {"result": "extra"}}}
				]
			}
		]
	}`)

	out := ConvertGeminiRequestToOpenAI("deepseek-v4-pro", input, false)

	messages := gjson.GetBytes(out, "messages")
	var toolCallIDs []string
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() == "tool" {
			id := msg.Get("tool_call_id").String()
			toolCallIDs = append(toolCallIDs, id)
		}
		return true
	})

	if len(toolCallIDs) != 3 {
		t.Fatalf("expected 3 tool messages, got %d", len(toolCallIDs))
	}

	seen := make(map[string]bool)
	for _, id := range toolCallIDs {
		if seen[id] {
			t.Errorf("duplicate tool_call_id %q", id)
		}
		seen[id] = true
	}
}
