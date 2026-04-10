package common

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseMessages(t *testing.T, raw string) []gjson.Result {
	t.Helper()
	parsed := gjson.Parse(raw)
	if !parsed.IsArray() {
		t.Fatalf("expected JSON array, got: %s", raw)
	}
	return parsed.Array()
}

func TestMergeAdjacentMessages_AssistantMergePreservesToolCalls(t *testing.T) {
	messages := parseMessages(t, `[
		{"role":"assistant","content":"part1"},
		{
			"role":"assistant",
			"content":"part2",
			"tool_calls":[
				{
					"id":"call_1",
					"type":"function",
					"function":{"name":"Read","arguments":"{}"}
				}
			]
		},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}
	]`)

	merged := MergeAdjacentMessages(messages)
	if len(merged) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d", len(merged))
	}

	assistant := merged[0]
	if assistant.Get("role").String() != "assistant" {
		t.Fatalf("expected first message role assistant, got %q", assistant.Get("role").String())
	}

	toolCalls := assistant.Get("tool_calls")
	if !toolCalls.IsArray() || len(toolCalls.Array()) != 1 {
		t.Fatalf("expected assistant.tool_calls length 1, got: %s", toolCalls.Raw)
	}
	if toolCalls.Array()[0].Get("id").String() != "call_1" {
		t.Fatalf("expected tool call id call_1, got %q", toolCalls.Array()[0].Get("id").String())
	}

	contentRaw := assistant.Get("content").Raw
	if !strings.Contains(contentRaw, "part1") || !strings.Contains(contentRaw, "part2") {
		t.Fatalf("expected merged content to contain both parts, got: %s", contentRaw)
	}

	if merged[1].Get("role").String() != "tool" {
		t.Fatalf("expected second message role tool, got %q", merged[1].Get("role").String())
	}
}

func TestMergeAdjacentMessages_AssistantMergeCombinesMultipleToolCalls(t *testing.T) {
	messages := parseMessages(t, `[
		{
			"role":"assistant",
			"content":"first",
			"tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}
			]
		},
		{
			"role":"assistant",
			"content":"second",
			"tool_calls":[
				{"id":"call_2","type":"function","function":{"name":"Write","arguments":"{}"}}
			]
		}
	]`)

	merged := MergeAdjacentMessages(messages)
	if len(merged) != 1 {
		t.Fatalf("expected 1 message after merge, got %d", len(merged))
	}

	toolCalls := merged[0].Get("tool_calls").Array()
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 merged tool calls, got %d", len(toolCalls))
	}
	if toolCalls[0].Get("id").String() != "call_1" || toolCalls[1].Get("id").String() != "call_2" {
		t.Fatalf("unexpected merged tool call ids: %q, %q", toolCalls[0].Get("id").String(), toolCalls[1].Get("id").String())
	}
}

func TestMergeAdjacentMessages_ToolMessagesRemainUnmerged(t *testing.T) {
	messages := parseMessages(t, `[
		{"role":"tool","tool_call_id":"call_1","content":"r1"},
		{"role":"tool","tool_call_id":"call_2","content":"r2"}
	]`)

	merged := MergeAdjacentMessages(messages)
	if len(merged) != 2 {
		t.Fatalf("expected tool messages to remain separate, got %d", len(merged))
	}
}
