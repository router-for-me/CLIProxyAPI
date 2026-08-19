package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeMessageAccumulatorGroupsAndOrdersAssistantParts(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"first","input":{}}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"answer"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"tool_use","id":"call_2","name":"second","input":{}}]}`))

	messages := accumulator.Messages()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	content := gjson.GetBytes(messages[0], "content").Array()
	wantTypes := []string{"thinking", "text", "tool_use", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("content count = %d, want %d. Message: %s", len(content), len(wantTypes), string(messages[0]))
	}
	for i, wantType := range wantTypes {
		if got := content[i].Get("type").String(); got != wantType {
			t.Fatalf("content[%d].type = %q, want %q", i, got, wantType)
		}
	}
	if got := content[2].Get("id").String(); got != "call_1" {
		t.Fatalf("first tool_use id = %q, want call_1", got)
	}
	if got := content[3].Get("id").String(); got != "call_2" {
		t.Fatalf("second tool_use id = %q, want call_2", got)
	}
}

func TestClaudeMessageAccumulatorPreservesUserOrderAndRoleBoundaries(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}`))
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"continue"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"done"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if got := gjson.GetBytes(messages[0], "role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}
	if got := gjson.GetBytes(messages[0], "content.0.type").String(); got != "tool_result" {
		t.Fatalf("first user block type = %q, want tool_result", got)
	}
	if got := gjson.GetBytes(messages[0], "content.1.text").String(); got != "continue" {
		t.Fatalf("second user block text = %q, want continue", got)
	}
	if got := gjson.GetBytes(messages[1], "role").String(); got != "assistant" {
		t.Fatalf("messages[1].role = %q, want assistant", got)
	}
}

func TestClaudeMessageAccumulatorSkipsEmptyMessagesWithoutBreakingTurn(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(3)
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"first"}]}`))
	accumulator.Append([]byte(`{"role":"user"}`))
	accumulator.Append([]byte(`{"role":"user","content":null}`))
	accumulator.Append([]byte(`{"role":"user","content":""}`))
	accumulator.Append([]byte(`{"role":"user","content":[]}`))
	accumulator.Append([]byte(`{"role":"invalid","content":[{"type":"text","text":"ignored"}]}`))
	accumulator.Append([]byte(`{"role":"assistant","content":[{"type":"text","text":"second"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if got := gjson.GetBytes(messages[0], "content.#").Int(); got != 2 {
		t.Fatalf("assistant content count = %d, want 2. Message: %s", got, string(messages[0]))
	}
}

func TestClaudeMessageAccumulatorFlushPreservesExplicitBoundary(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(2)
	accumulator.Append([]byte(`{"role":"user","content":"system reminder"}`))
	accumulator.Flush()
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"question"}]}`))

	messages := accumulator.Messages()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if got := gjson.GetBytes(messages[0], "content.0.text").String(); got != "system reminder" {
		t.Fatalf("first message text = %q, want system reminder", got)
	}
	if got := gjson.GetBytes(messages[1], "content.0.text").String(); got != "question" {
		t.Fatalf("second message text = %q, want question", got)
	}
}

func TestClaudeMessageAccumulatorPreservesBlockCacheControl(t *testing.T) {
	accumulator := NewClaudeMessageAccumulator(2)
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"cached","cache_control":{"type":"ephemeral"}}]}`))
	accumulator.Append([]byte(`{"role":"user","content":[{"type":"text","text":"fresh"}]}`))

	messages := accumulator.Messages()
	if got := gjson.GetBytes(messages[0], "content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("cache_control.type = %q, want ephemeral", got)
	}
	if gjson.GetBytes(messages[0], "content.1.cache_control").Exists() {
		t.Fatalf("second block should not have cache_control: %s", string(messages[0]))
	}
}
