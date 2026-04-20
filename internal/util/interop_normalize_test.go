package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesRequestJSON_ConvertsClaudeBlocks(t *testing.T) {
	input := []byte(`{
		"input":[
			{
				"role":"assistant",
				"content":[
					{"type":"text","text":"checking"},
					{"type":"tool_use","id":"call_1","name":"sessions_list","input":{"limit":10}}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			}
		]
	}`)

	out := NormalizeOpenAIResponsesRequestJSON(input)
	items := gjson.GetBytes(out, "input").Array()
	if len(items) != 4 {
		t.Fatalf("expected 4 normalized items, got %d: %s", len(items), gjson.GetBytes(out, "input").Raw)
	}
	if items[1].Get("type").String() != "function_call" {
		t.Fatalf("expected item 1 function_call, got %s", items[1].Raw)
	}
	if items[2].Get("type").String() != "message" || items[3].Get("type").String() != "function_call_output" {
		t.Fatalf("expected message + function_call_output tail: %s", gjson.GetBytes(out, "input").Raw)
	}
}

func TestNormalizeOpenAIChatRequestJSON_ConvertsClaudeBlocks(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"text","text":"checking"},
					{"type":"tool_use","id":"call_1","name":"sessions_list","input":{"limit":10}},
					{"type":"thinking","thinking":"internal"}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"}
				]
			}
		]
	}`)

	out := NormalizeOpenAIChatRequestJSON(input)
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 normalized messages, got %d: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if !msgs[0].Get("tool_calls").IsArray() {
		t.Fatalf("assistant tool_calls should be synthesized: %s", msgs[0].Raw)
	}
	if got := msgs[0].Get("reasoning_content").String(); got != "internal" {
		t.Fatalf("expected reasoning_content=internal, got %q", got)
	}
	if got := msgs[0].Get("content.0.text").String(); got != "checking" {
		t.Fatalf("expected assistant text to be preserved, got %q", got)
	}
	if got := msgs[1].Get("role").String(); got != "tool" {
		t.Fatalf("expected appended tool role, got %q: %s", got, msgs[1].Raw)
	}
}

func TestNormalizeOpenAIChatRequestJSON_PlacesToolResultBeforeUserText(t *testing.T) {
	input := []byte(`{
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"tool_use","id":"call_1","name":"sessions_list","input":{"limit":10}}
				]
			},
			{
				"role":"user",
				"content":[
					{"type":"tool_result","tool_use_id":"call_1","content":"ok"},
					{"type":"text","text":"continue"}
				]
			}
		]
	}`)

	out := NormalizeOpenAIChatRequestJSON(input)
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 normalized messages, got %d: %s", len(msgs), gjson.GetBytes(out, "messages").Raw)
	}
	if got := msgs[1].Get("role").String(); got != "tool" {
		t.Fatalf("expected tool result to immediately follow assistant tool_calls, got %q: %s", got, msgs[1].Raw)
	}
	if got := msgs[1].Get("tool_call_id").String(); got != "call_1" {
		t.Fatalf("expected tool_call_id call_1, got %q", got)
	}
	if got := msgs[2].Get("role").String(); got != "user" {
		t.Fatalf("expected trailing user message after tool result, got %q: %s", got, msgs[2].Raw)
	}
	if got := msgs[2].Get("content.0.text").String(); got != "continue" {
		t.Fatalf("expected user text to remain after tool result, got %q", got)
	}
}
