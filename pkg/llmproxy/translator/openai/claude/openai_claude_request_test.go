package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAI(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-sonnet",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"system": "be helpful",
		"thinking": {"type": "enabled", "budget_tokens": 1024}
	}`)

	got := ConvertClaudeRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", res.Get("model").String())
	}

	if res.Get("max_tokens").Int() != 1024 {
		t.Errorf("expected max_tokens 1024, got %d", res.Get("max_tokens").Int())
	}

	// OpenAI format for system message is role: system, content: string or array
	// Our translator converts it to role: system, content: [{type: text, text: ...}]
	messages := res.Get("messages").Array()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Get("role").String() != "system" {
		t.Errorf("expected first message role system, got %s", messages[0].Get("role").String())
	}

	if messages[1].Get("role").String() != "user" {
		t.Errorf("expected second message role user, got %s", messages[1].Get("role").String())
	}

	// Check thinking conversion
	if res.Get("reasoning_effort").String() == "" {
		t.Error("expected reasoning_effort to be set")
	}
}

func TestConvertClaudeRequestToOpenAI_SystemArray(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-sonnet",
		"system": [
			{"type": "text", "text": "be helpful"},
			{"type": "text", "text": "and polite"}
		],
		"messages": [{"role": "user", "content": "hello"}]
	}`)

	got := ConvertClaudeRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Errorf("expected 2 system content parts, got %d", len(content))
	}

	if content[0].Get("text").String() != "be helpful" {
		t.Errorf("expected first system part be helpful, got %s", content[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToOpenAI_FullMessage(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-sonnet",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "describe this"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "abc"}}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me see..."},
					{"type": "text", "text": "This is a cat."},
					{"type": "tool_use", "id": "call_1", "name": "get_cat_details", "input": {"cat_id": 1}}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_1", "content": "cat info"}
				]
			}
		],
		"tools": [
			{"name": "get_cat_details", "description": "Get details about a cat", "input_schema": {"type": "object", "properties": {"cat_id": {"type": "integer"}}}}
		]
	}`)

	got := ConvertClaudeRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	messages := res.Get("messages").Array()
	// user + assistant (thinking, text, tool_use) + tool_result
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	// First message: user with image
	content1 := messages[0].Get("content").Array()
	if len(content1) != 2 {
		t.Errorf("expected 2 user content parts, got %d", len(content1))
	}
	if content1[1].Get("type").String() != "image_url" {
		t.Errorf("expected image_url part, got %s", content1[1].Get("type").String())
	}

	// Second message: assistant with reasoning, content, tool_calls
	if messages[1].Get("role").String() != "assistant" {
		t.Errorf("expected second message role assistant, got %s", messages[1].Get("role").String())
	}
	if messages[1].Get("reasoning_content").String() != "Let me see..." {
		t.Errorf("expected reasoning_content Let me see..., got %s", messages[1].Get("reasoning_content").String())
	}
	if messages[1].Get("tool_calls").Array()[0].Get("function.name").String() != "get_cat_details" {
		t.Errorf("expected tool call get_cat_details, got %s", messages[1].Get("tool_calls").Array()[0].Get("function.name").String())
	}

	// Third message: tool result
	if messages[2].Get("role").String() != "tool" {
		t.Errorf("expected third message role tool, got %s", messages[2].Get("role").String())
	}
	if messages[2].Get("content").String() != "cat info" {
		t.Errorf("expected tool result content cat info, got %s", messages[2].Get("content").String())
	}

	// Check tools
	tools := res.Get("tools").Array()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Get("function.name").String() != "get_cat_details" {
		t.Errorf("expected tool get_cat_details, got %s", tools[0].Get("function.name").String())
	}
}

func TestConvertClaudeRequestToOpenAI_ToolChoice(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-sonnet",
		"messages": [{"role": "user", "content": "hello"}],
		"tool_choice": {"type": "tool", "name": "my_tool"}
	}`)

	got := ConvertClaudeRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("tool_choice.function.name").String() != "my_tool" {
		t.Errorf("expected tool_choice function name my_tool, got %s", res.Get("tool_choice.function.name").String())
	}
}

func TestConvertClaudeRequestToOpenAI_Params(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-sonnet",
		"messages": [{"role": "user", "content": "hello"}],
		"temperature": 0.5,
		"stop_sequences": ["STOP"],
		"user": "u123"
	}`)

	got := ConvertClaudeRequestToOpenAI("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	if res.Get("temperature").Float() != 0.5 {
		t.Errorf("expected temperature 0.5, got %f", res.Get("temperature").Float())
	}
	if res.Get("stop").String() != "STOP" {
		t.Errorf("expected stop STOP, got %s", res.Get("stop").String())
	}
	if res.Get("user").String() != "u123" {
		t.Errorf("expected user u123, got %s", res.Get("user").String())
	}
}
