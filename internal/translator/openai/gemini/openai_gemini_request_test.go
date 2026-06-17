package gemini

import (
	"testing"

	"github.com/tidwall/gjson"
)

func collectToolCallIDs(messages gjson.Result) ([]string, []string) {
	var assistantToolCallIDs []string
	var toolMessageIDs []string

	messages.ForEach(func(_, msg gjson.Result) bool {
		switch msg.Get("role").String() {
		case "assistant":
			msg.Get("tool_calls").ForEach(func(_, toolCall gjson.Result) bool {
				assistantToolCallIDs = append(assistantToolCallIDs, toolCall.Get("id").String())
				return true
			})
		case "tool":
			toolMessageIDs = append(toolMessageIDs, msg.Get("tool_call_id").String())
		}
		return true
	})

	return assistantToolCallIDs, toolMessageIDs
}

func assertDistinctToolCallIDs(t *testing.T, toolMessageIDs []string) {
	t.Helper()

	seen := make(map[string]bool)
	for _, id := range toolMessageIDs {
		if seen[id] {
			t.Errorf("duplicate tool_call_id %q", id)
		}
		seen[id] = true
	}
}

func assertToolCallIDsMatch(t *testing.T, assistantToolCallIDs, toolMessageIDs []string, count int) {
	t.Helper()

	for i := 0; i < count; i++ {
		if assistantToolCallIDs[i] != toolMessageIDs[i] {
			t.Errorf("mismatch at index %d: assistant tool call ID %q != tool message ID %q", i, assistantToolCallIDs[i], toolMessageIDs[i])
		}
	}
}

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

	assistantToolCallIDs, toolMessageIDs := collectToolCallIDs(messages)

	if len(assistantToolCallIDs) != 2 {
		t.Fatalf("expected 2 assistant tool calls, got %d", len(assistantToolCallIDs))
	}
	if len(toolMessageIDs) != 2 {
		t.Fatalf("expected 2 tool messages, got %d", len(toolMessageIDs))
	}

	assertToolCallIDsMatch(t, assistantToolCallIDs, toolMessageIDs, 2)
	assertDistinctToolCallIDs(t, toolMessageIDs)
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
	assistantToolCallIDs, toolMessageIDs := collectToolCallIDs(messages)

	if len(assistantToolCallIDs) != 3 {
		t.Fatalf("expected 3 assistant tool calls, got %d", len(assistantToolCallIDs))
	}
	if len(toolMessageIDs) != 3 {
		t.Fatalf("expected 3 tool messages, got %d", len(toolMessageIDs))
	}

	assertToolCallIDsMatch(t, assistantToolCallIDs, toolMessageIDs, 3)
	assertDistinctToolCallIDs(t, toolMessageIDs)
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
	assistantToolCallIDs, toolMessageIDs := collectToolCallIDs(messages)

	if len(assistantToolCallIDs) != 0 {
		t.Fatalf("expected 0 assistant tool calls, got %d", len(assistantToolCallIDs))
	}
	if len(toolMessageIDs) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(toolMessageIDs))
	}
	if toolMessageIDs[0] == "" {
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
	assistantToolCallIDs, toolMessageIDs := collectToolCallIDs(messages)

	if len(assistantToolCallIDs) != 2 {
		t.Fatalf("expected 2 assistant tool calls, got %d", len(assistantToolCallIDs))
	}
	if len(toolMessageIDs) != 3 {
		t.Fatalf("expected 3 tool messages, got %d", len(toolMessageIDs))
	}

	assertToolCallIDsMatch(t, assistantToolCallIDs, toolMessageIDs, 2)
	assertDistinctToolCallIDs(t, toolMessageIDs)
}
