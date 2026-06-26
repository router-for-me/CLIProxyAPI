package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
		"input": [
			{
				"role": "user",
				"content": [
					{"type": "input_text", "text": "hello"}
				]
			}
		],
		"max_output_tokens": 100
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "claude-3-5-sonnet" {
		t.Errorf("expected model claude-3-5-sonnet, got %s", res.Get("model").String())
	}

	if res.Get("max_tokens").Int() != 100 {
		t.Errorf("expected max_tokens 100, got %d", res.Get("max_tokens").Int())
	}

	messages := res.Get("messages").Array()
	if len(messages) < 1 {
		t.Errorf("expected at least 1 message, got %d", len(messages))
	}
}

func TestConvertOpenAIResponsesRequestToClaudeToolChoice(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"tool_choice": "required",
		"tools": [{
			"type": "function",
			"name": "weather",
			"description": "Get weather",
			"parameters": {"type":"object","properties":{"city":{"type":"string"}}}
		}]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("tool_choice.type").String() != "any" {
		t.Fatalf("tool_choice.type = %s, want any", res.Get("tool_choice.type").String())
	}

	if res.Get("max_tokens").Int() != 32000 {
		t.Fatalf("expected default max_tokens to remain, got %d", res.Get("max_tokens").Int())
	}
}

func TestConvertOpenAIResponsesRequestToClaudeFunctionCallOutput(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet",
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{\"city\":\"sf\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"\"cloudy\""}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(messages))
	}

	last := messages[len(messages)-1]
	if last.Get("role").String() != "user" {
		t.Fatalf("last message role = %s, want user", last.Get("role").String())
	}
	if last.Get("content.0.type").String() != "tool_result" {
		t.Fatalf("last content type = %s, want tool_result", last.Get("content.0.type").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaudeStringInputBody(t *testing.T) {
	input := []byte(`{"model":"claude-3-5-sonnet","input":"hello"}`)
	got := ConvertOpenAIResponsesRequestToClaude("claude-3-5-sonnet", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}
	if messages[0].Get("role").String() != "user" {
		t.Fatalf("message role = %s, want user", messages[0].Get("role").String())
	}
	if messages[0].Get("content").String() != "hello" {
		t.Fatalf("message content = %q, want hello", messages[0].Get("content").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaude_PreservesReasoningBeforeToolUse(t *testing.T) {
	input := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"input": [
			{
				"type":"reasoning",
				"summary":[{"type":"summary_text","text":"I should call weather tool"}]
			},
			{
				"type":"function_call",
				"call_id":"call-1",
				"name":"weather",
				"arguments":"{\"city\":\"sf\"}"
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-6-thinking", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(messages))
	}

	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Fatalf("assistant content len = %d, want 2", len(content))
	}
	if content[0].Get("type").String() != "redacted_thinking" {
		t.Fatalf("first content type = %s, want redacted_thinking", content[0].Get("type").String())
	}
	if content[0].Get("data").String() != "I should call weather tool" {
		t.Fatalf("redacted_thinking data = %q", content[0].Get("data").String())
	}
	if content[1].Get("type").String() != "tool_use" {
		t.Fatalf("second content type = %s, want tool_use", content[1].Get("type").String())
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SanitizesThinkingSignature(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-6",
		"input":[
			{
				"type":"message",
				"role":"assistant",
				"content":[
					{"type":"thinking","thinking":"prior provider reasoning","signature":"invalid-signature"},
					{"type":"output_text","text":"tool call next"}
				]
			}
		]
	}`)

	got := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-6", input, false)
	res := gjson.ParseBytes(got)

	first := res.Get("messages.0.content.0")
	if first.Get("type").String() != "redacted_thinking" {
		t.Fatalf("first content type = %s, want redacted_thinking", first.Get("type").String())
	}
	if first.Get("data").String() != "prior provider reasoning" {
		t.Fatalf("redacted thinking data = %q", first.Get("data").String())
	}
	if first.Get("signature").Exists() {
		t.Fatal("redacted_thinking must not carry signature")
	}
}
