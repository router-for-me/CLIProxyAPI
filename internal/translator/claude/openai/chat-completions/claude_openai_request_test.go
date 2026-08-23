package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToClaudeWithCompat_GroupsAssistantThinkingTextAndTools(t *testing.T) {
	inputJSON := []byte(`{
		"messages":[
			{"role":"assistant","reasoning_content":"reason","content":"answer"},
			{
				"role":"assistant",
				"content":"",
				"tool_calls":[
					{"id":"call_1","type":"function","function":{"name":"first","arguments":"{}"}},
					{"id":"call_2","type":"function","function":{"name":"second","arguments":"{}"}}
				]
			}
		]
	}`)
	out := ConvertOpenAIRequestToClaudeWithCompat("claude-test", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1. Output: %s", len(messages), string(out))
	}
	content := messages[0].Get("content").Array()
	wantTypes := []string{"thinking", "text", "tool_use", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("content count = %d, want %d. Output: %s", len(content), len(wantTypes), string(out))
	}
	for i, wantType := range wantTypes {
		if got := content[i].Get("type").String(); got != wantType {
			t.Fatalf("content[%d].type = %q, want %q", i, got, wantType)
		}
	}
}

func TestConvertOpenAIRequestToClaude_MergesToolResultWithAdjacentUserContent(t *testing.T) {
	inputJSON := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"work","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"user","content":"continue"}
		]
	}`)
	out := ConvertOpenAIRequestToClaude("claude-test", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2. Output: %s", len(messages), string(out))
	}
	userContent := messages[1].Get("content").Array()
	if len(userContent) != 2 {
		t.Fatalf("user content count = %d, want 2. Output: %s", len(userContent), string(out))
	}
	if got := userContent[0].Get("type").String(); got != "tool_result" {
		t.Fatalf("user content[0].type = %q, want tool_result", got)
	}
	if got := userContent[1].Get("text").String(); got != "continue" {
		t.Fatalf("user content[1].text = %q, want continue", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemDoesNotBreakUserTurnAndCacheBoundary(t *testing.T) {
	inputJSON := []byte(`{
		"messages":[
			{"role":"user","content":"first","cache_control":{"type":"ephemeral"}},
			{"role":"system","content":"system rule"},
			{"role":"user","content":"second"}
		]
	}`)
	out := ConvertOpenAIRequestToClaude("claude-test", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1. Output: %s", len(messages), string(out))
	}
	content := messages[0].Get("content").Array()
	if len(content) != 2 {
		t.Fatalf("content count = %d, want 2. Output: %s", len(content), string(out))
	}
	if got := content[0].Get("text").String(); got != "first" {
		t.Fatalf("content[0].text = %q, want first", got)
	}
	if got := content[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content[0].cache_control.type = %q, want ephemeral", got)
	}
	if got := content[1].Get("text").String(); got != "second" {
		t.Fatalf("content[1].text = %q, want second", got)
	}
	if got := gjson.GetBytes(out, "system.0.text").String(); got != "system rule" {
		t.Fatalf("system text = %q, want system rule", got)
	}
}

func TestConvertOpenAIRequestToClaude_SanitizesToolCallIDsForClaude(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call.with space:1",
						"type": "function",
						"function": {
							"name": "Read",
							"arguments": "{\"path\":\"README.md\"}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call.with space:1",
				"content": "ok"
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	toolUseID := resultJSON.Get("messages.0.content.0.id").String()
	toolResultID := resultJSON.Get("messages.1.content.0.tool_use_id").String()

	if toolUseID != "call_with_space_1" {
		t.Fatalf("tool_use id = %q, want %q", toolUseID, "call_with_space_1")
	}
	if toolResultID != toolUseID {
		t.Fatalf("tool_result tool_use_id = %q, want same sanitized id %q", toolResultID, toolUseID)
	}
}

func TestConvertOpenAIRequestToClaude_GroupsConsecutiveParallelToolResults(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "user", "content": "Use both tools."},
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "tool_a", "arguments": "{}"}},
					{"id": "call_2", "type": "function", "function": {"name": "tool_b", "arguments": "{}"}}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "one",
				"cache_control": {"type": "ephemeral"}
			},
			{"role": "tool", "tool_call_id": "call_2", "content": "two"},
			{"role": "assistant", "content": "Done."}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 4 {
		t.Fatalf("Expected 4 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("Expected grouped tool result role %q, got %q", "user", got)
	}
	toolResults := messages[2].Get("content").Array()
	if len(toolResults) != 2 {
		t.Fatalf("Expected 2 grouped tool results, got %d. Content: %s", len(toolResults), messages[2].Get("content").Raw)
	}
	wants := []struct {
		id      string
		content string
	}{
		{id: "call_1", content: "one"},
		{id: "call_2", content: "two"},
	}
	for i, want := range wants {
		if got := toolResults[i].Get("type").String(); got != "tool_result" {
			t.Fatalf("tool result %d type = %q, want tool_result", i, got)
		}
		if got := toolResults[i].Get("tool_use_id").String(); got != want.id {
			t.Fatalf("tool result %d tool_use_id = %q, want %q", i, got, want.id)
		}
		if got := toolResults[i].Get("content").String(); got != want.content {
			t.Fatalf("tool result %d content = %q, want %q", i, got, want.content)
		}
	}
	if got := toolResults[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("first tool result cache_control.type = %q, want ephemeral", got)
	}
	if got := messages[3].Get("content.0.text").String(); got != "Done." {
		t.Fatalf("following assistant message text = %q, want Done.", got)
	}
}

func TestConvertOpenAIRequestToClaude_DropsTemperature(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"temperature": 0.2,
		"top_p": 0.8,
		"messages": [
			{"role": "user", "content": "hi"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if resultJSON.Get("temperature").Exists() {
		t.Fatalf("temperature should be removed")
	}
	if got := resultJSON.Get("top_p").Float(); got != 0.8 {
		t.Fatalf("top_p = %v, want 0.8", got)
	}
}

func TestConvertOpenAIRequestToClaude_ToolResultTextAndBase64Image(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{"type": "text", "text": "tool ok"},
					{
						"type": "image_url",
						"image_url": {
							"url": "data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolResult := messages[1].Get("content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("Expected content[0].type %q, got %q", "tool_result", got)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_1" {
		t.Fatalf("Expected tool_use_id %q, got %q", "call_1", got)
	}

	toolContent := toolResult.Get("content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected first tool_result part type %q, got %q", "text", got)
	}
	if got := toolContent.Get("0.text").String(); got != "tool ok" {
		t.Fatalf("Expected first tool_result part text %q, got %q", "tool ok", got)
	}
	if got := toolContent.Get("1.type").String(); got != "image" {
		t.Fatalf("Expected second tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("1.source.type").String(); got != "base64" {
		t.Fatalf("Expected image source type %q, got %q", "base64", got)
	}
	if got := toolContent.Get("1.source.media_type").String(); got != "image/png" {
		t.Fatalf("Expected image media type %q, got %q", "image/png", got)
	}
	if got := toolContent.Get("1.source.data").String(); got != "iVBORw0KGgoAAAANSUhEUg==" {
		t.Fatalf("Unexpected base64 image data: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_ToolResultURLImageOnly(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "do_work",
							"arguments": "{\"a\":1}"
						}
					}
				]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": [
					{
						"type": "image_url",
						"image_url": {
							"url": "https://example.com/tool.png"
						}
					}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	messages := resultJSON.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}

	toolContent := messages[1].Get("content.0.content")
	if !toolContent.IsArray() {
		t.Fatalf("Expected tool_result content array, got %s", toolContent.Raw)
	}
	if got := toolContent.Get("0.type").String(); got != "image" {
		t.Fatalf("Expected tool_result part type %q, got %q", "image", got)
	}
	if got := toolContent.Get("0.source.type").String(); got != "url" {
		t.Fatalf("Expected image source type %q, got %q", "url", got)
	}
	if got := toolContent.Get("0.source.url").String(); got != "https://example.com/tool.png" {
		t.Fatalf("Unexpected image URL: %q", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemRoleBecomesTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system")
	if !system.IsArray() {
		t.Fatalf("Expected top-level system array, got %s", system.Raw)
	}
	if len(system.Array()) != 1 {
		t.Fatalf("Expected 1 system block, got %d. System: %s", len(system.Array()), system.Raw)
	}
	if got := system.Get("0.type").String(); got != "text" {
		t.Fatalf("Expected system block type %q, got %q", "text", got)
	}
	if got := system.Get("0.text").String(); got != "You are a helpful assistant." {
		t.Fatalf("Expected system text %q, got %q", "You are a helpful assistant.", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}

func TestConvertOpenAIRequestToClaude_MultipleSystemMessagesMergedIntoTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "Rule 1"},
			{"role": "system", "content": [{"type": "text", "text": "Rule 2"}]},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("Expected 2 system blocks, got %d. System: %s", len(system), resultJSON.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "Rule 1" {
		t.Fatalf("Expected first system text %q, got %q", "Rule 1", got)
	}
	if got := system[1].Get("text").String(); got != "Rule 2" {
		t.Fatalf("Expected second system text %q, got %q", "Rule 2", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 non-system message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected remaining message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "Hello" {
		t.Fatalf("Expected user text %q, got %q", "Hello", got)
	}
}

func TestConvertOpenAIRequestToClaude_SystemOnlyInputKeepsFallbackUserMessage(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 1 {
		t.Fatalf("Expected 1 system block, got %d. System: %s", len(system), resultJSON.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "You are a helpful assistant." {
		t.Fatalf("Expected system text %q, got %q", "You are a helpful assistant.", got)
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("Expected 1 fallback message, got %d. Messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("Expected fallback message role %q, got %q", "user", got)
	}
	if got := messages[0].Get("content.0.type").String(); got != "text" {
		t.Fatalf("Expected fallback content type %q, got %q", "text", got)
	}
	if got := messages[0].Get("content.0.text").String(); got != "" {
		t.Fatalf("Expected fallback text %q, got %q", "", got)
	}
}

func TestConvertOpenAIRequestToClaude_PreservesContentPartCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "cached prefix", "cache_control": {"type": "ephemeral"}},
					{"type": "text", "text": "fresh question"}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if resultJSON.Get("messages.0.content.1.cache_control").Exists() {
		t.Fatalf("content.1 should not have cache_control. Output: %s", result)
	}
	if got := resultJSON.Get("messages.0.content.0.text").String(); got != "cached prefix" {
		t.Fatalf("content.0.text = %q, want %q", got, "cached prefix")
	}
}

func TestConvertOpenAIRequestToClaude_PreservesMessageLevelCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"content": "cache me",
				"cache_control": {"type": "ephemeral", "ttl": "1h"}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if got := resultJSON.Get("messages.0.content.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("content.0.cache_control.ttl = %q, want 1h. Output: %s", got, result)
	}
}

func TestConvertOpenAIRequestToClaude_PreservesToolCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "lookup",
					"description": "Lookup something",
					"parameters": {"type": "object", "properties": {}}
				},
				"cache_control": {"type": "ephemeral"}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tools.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if got := resultJSON.Get("tools.0.name").String(); got != "lookup" {
		t.Fatalf("tools.0.name = %q, want lookup", got)
	}
}

func TestConvertOpenAIRequestToClaude_NormalizesRootToolSchemaUnions(t *testing.T) {
	inputJSON := `{
		"model":"claude-sonnet-4-5",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"without_type",
					"parameters":{
						"anyOf":[
							{"type":"object","properties":{"a":{"type":"string"}}},
							{"type":"object","properties":{"b":{"type":"string"}}}
						]
					}
				}
			},
			{
				"type":"function",
				"function":{
					"name":"constraint_union",
					"parametersJsonSchema":{
						"type":"object",
						"properties":{"a":{"type":"string"},"b":{"type":"string"}},
						"anyOf":[{"required":["a"]},{"required":["b"]}]
					}
				}
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	root := gjson.ParseBytes(result)

	for _, toolName := range []string{"without_type", "constraint_union"} {
		schema := root.Get(`tools.#(name=="` + toolName + `").input_schema`)
		if got := schema.Get("type").String(); got != "object" {
			t.Fatalf("%s input_schema.type = %q, want object. Output: %s", toolName, got, result)
		}
		if schema.Get("anyOf").Exists() {
			t.Fatalf("%s input_schema should not contain root anyOf. Output: %s", toolName, result)
		}
		if !schema.Get("properties.a").Exists() || !schema.Get("properties.b").Exists() {
			t.Fatalf("%s input_schema should contain properties a and b. Output: %s", toolName, result)
		}
		if schema.Get("required").Exists() {
			t.Fatalf("%s input_schema should not merge alternative required fields. Output: %s", toolName, result)
		}
	}
}

func TestConvertOpenAIRequestToClaude_PartCacheControlWinsOverMessageLevel(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{
				"role": "user",
				"cache_control": {"type": "ephemeral", "ttl": "1h"},
				"content": [
					{"type": "text", "text": "part cached", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral. Output: %s", got, result)
	}
	if resultJSON.Get("messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("part-level cache_control should win; unexpected ttl: %s", result)
	}
}

func TestConvertOpenAIRequestToClaude_DeveloperRoleBecomesTopLevelSystem(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "system", "content": "S1"},
			{"role": "developer", "content": [{"type": "text", "text": "D1"}, {"type": "text", "text": "D2"}]},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	system := resultJSON.Get("system").Array()
	if len(system) != 3 {
		t.Fatalf("system blocks = %d, want 3. system: %s", len(system), resultJSON.Get("system").Raw)
	}
	for idx, want := range []string{"S1", "D1", "D2"} {
		if got := system[idx].Get("type").String(); got != "text" {
			t.Fatalf("system[%d].type = %q, want text", idx, got)
		}
		if got := system[idx].Get("text").String(); got != want {
			t.Fatalf("system[%d].text = %q, want %q", idx, got, want)
		}
	}

	messages := resultJSON.Get("messages").Array()
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1. messages: %s", len(messages), resultJSON.Get("messages").Raw)
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want user", got)
	}
}

func TestConvertOpenAIRequestToClaude_DeveloperMessageCacheControlAppliesToLastBlock(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"messages": [
			{"role": "developer", "content": [{"type": "text", "text": "D1"}, {"type": "text", "text": "D2"}], "cache_control": {"type": "ephemeral"}},
			{"role": "user", "content": "Hello"}
		]
	}`

	result := ConvertOpenAIRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	system := gjson.ParseBytes(result).Get("system").Array()
	if len(system) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(system))
	}
	if system[0].Get("cache_control").Exists() {
		t.Fatalf("system[0] must not carry cache_control: %s", system[0].Raw)
	}
	if got := system[1].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system[1].cache_control.type = %q, want ephemeral", got)
	}
}

func TestConvertOpenAIRequestToClaude_DeduplicatesToolResults(t *testing.T) {
	inputJSON := []byte(`{
		"messages":[
			{"role":"user","content":"Run tools"},
			{"role":"assistant","tool_calls":[
				{"id":"call_dup","type":"function","function":{"name":"lookup","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_dup","content":"first output"},
			{"role":"assistant","content":"Next step","tool_calls":[
				{"id":"call_other","type":"function","function":{"name":"search","arguments":"{}"}}
			]},
			{"role":"tool","tool_call_id":"call_dup","content":"final output"},
			{"role":"tool","tool_call_id":"call_other","content":"search output"},
			{"role":"tool","tool_call_id":"","content":"empty id output"}
		]
	}`)
	out := ConvertOpenAIRequestToClaude("claude-test", inputJSON, false)
	root := gjson.ParseBytes(out)

	messages := root.Get("messages").Array()
	if len(messages) < 5 {
		t.Fatalf("expected at least 5 messages, got %d. Output: %s", len(messages), string(out))
	}

	// Message 1: assistant tool_use call_dup
	if got := messages[1].Get("content.0.id").String(); got != "call_dup" {
		t.Fatalf("messages[1].content.0.id = %q, want call_dup", got)
	}

	// Message 2: user tool_result for call_dup with final payload, before assistant message 3
	if got := messages[2].Get("content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages[2].content.0.type = %q, want tool_result", got)
	}
	if got := messages[2].Get("content.0.tool_use_id").String(); got != "call_dup" {
		t.Fatalf("messages[2].content.0.tool_use_id = %q, want call_dup", got)
	}
	if got := messages[2].Get("content.0.content").String(); got != "final output" {
		t.Fatalf("messages[2].content.0.content = %q, want 'final output'", got)
	}

	// Message 3: assistant Next step + tool_use call_other
	if got := messages[3].Get("content.0.text").String(); got != "Next step" {
		t.Fatalf("messages[3].content.0.text = %q, want 'Next step'", got)
	}
	if got := messages[3].Get("content.1.id").String(); got != "call_other" {
		t.Fatalf("messages[3].content.1.id = %q, want call_other", got)
	}

	// Message 4: user tool_results for call_other (search output) and empty id output; call_dup should NOT be repeated here
	msg4Blocks := messages[4].Get("content").Array()
	if len(msg4Blocks) != 2 {
		t.Fatalf("expected 2 tool_result blocks in message 4, got %d. Output: %s", len(msg4Blocks), string(out))
	}
	if got := msg4Blocks[0].Get("tool_use_id").String(); got != "call_other" {
		t.Fatalf("msg4Blocks[0].tool_use_id = %q, want call_other", got)
	}
	if got := msg4Blocks[0].Get("content").String(); got != "search output" {
		t.Fatalf("msg4Blocks[0].content = %q, want 'search output'", got)
	}
	if got := msg4Blocks[1].Get("content").String(); got != "empty id output" {
		t.Fatalf("msg4Blocks[1].content = %q, want 'empty id output'", got)
	}
}

func TestConvertOpenAIRequestToClaude_MaxTokensAndMaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name      string
		rawJSON   string
		wantLimit int64
	}{
		{
			name:      "only max_completion_tokens",
			rawJSON:   `{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":128000}`,
			wantLimit: 128000,
		},
		{
			name:      "only max_tokens",
			rawJSON:   `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096}`,
			wantLimit: 4096,
		},
		{
			name:      "both present prefers max_tokens",
			rawJSON:   `{"messages":[{"role":"user","content":"hi"}],"max_tokens":4096,"max_completion_tokens":128000}`,
			wantLimit: 4096,
		},
		{
			name:      "neither present uses default template limit",
			rawJSON:   `{"messages":[{"role":"user","content":"hi"}]}`,
			wantLimit: 32000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIRequestToClaude("claude-3-7-sonnet-20250219", []byte(tc.rawJSON), false)
			got := gjson.GetBytes(out, "max_tokens").Int()
			if got != tc.wantLimit {
				t.Fatalf("max_tokens = %d, want %d. Output: %s", got, tc.wantLimit, string(out))
			}
		})
	}
}

func TestConvertOpenAIRequestToClaude_PreservesCallerSuppliedMetadataUserID(t *testing.T) {
	testCases := []struct {
		name     string
		rawJSON  string
		expected string
	}{
		{
			name:     "plain string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"custom-user-123"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: "custom-user-123",
		},
		{
			name:     "special characters and json string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"foo\"bar\nbaz\\qux"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: "foo\"bar\nbaz\\qux",
		},
		{
			name:     "claude code json format",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"{\"device_id\":\"0000000000000000000000000000000000000000000000000000000000000000\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","session_id":"11111111-2222-4333-8444-555555555555"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIRequestToClaude("claude-test", []byte(tc.rawJSON), false)
			if !gjson.ValidBytes(out) {
				t.Fatalf("output is invalid json: %s", string(out))
			}
			got := gjson.GetBytes(out, "metadata.user_id").String()
			if got != tc.expected {
				t.Fatalf("metadata.user_id = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestConvertOpenAIRequestToClaude_PreservesOpenAIUserField(t *testing.T) {
	raw := []byte(`{"model":"claude-test","user":"openai-user-456","messages":[{"role":"user","content":"hello"}]}`)
	out := ConvertOpenAIRequestToClaude("claude-test", raw, false)
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is invalid json: %s", string(out))
	}
	got := gjson.GetBytes(out, "metadata.user_id").String()
	if got != "openai-user-456" {
		t.Fatalf("metadata.user_id = %q, want %q", got, "openai-user-456")
	}
}

func TestConvertOpenAIRequestToClaude_DifferentSessionsProduceDifferentUserIDs(t *testing.T) {
	a := []byte(`{"model":"claude-test","prompt_cache_key":"session-a","messages":[{"role":"user","content":"hello"}]}`)
	b := []byte(`{"model":"claude-test","prompt_cache_key":"session-b","messages":[{"role":"user","content":"hello"}]}`)
	outA := ConvertOpenAIRequestToClaude("claude-test", a, false)
	outB := ConvertOpenAIRequestToClaude("claude-test", b, false)
	idA := gjson.GetBytes(outA, "metadata.user_id").String()
	idB := gjson.GetBytes(outB, "metadata.user_id").String()
	if idA == idB {
		t.Fatalf("different prompt_cache_key produced identical metadata.user_id: %q", idA)
	}
}

func TestConvertOpenAIRequestToClaude_DeterministicWithoutSessionKey(t *testing.T) {
	first := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"stable first message"}]}`)
	second := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"stable first message"},{"role":"assistant","content":"hi"},{"role":"user","content":"second message"}]}`)
	outFirst := ConvertOpenAIRequestToClaude("claude-test", first, false)
	outSecond := ConvertOpenAIRequestToClaude("claude-test", second, false)
	idFirst := gjson.GetBytes(outFirst, "metadata.user_id").String()
	idSecond := gjson.GetBytes(outSecond, "metadata.user_id").String()
	if idFirst == "" || idFirst == "unknown" {
		t.Fatalf("expected non-empty derived user_id, got %q", idFirst)
	}
	if idFirst != idSecond {
		t.Fatalf("turn growth changed derived user_id: %q vs %q", idFirst, idSecond)
	}
}
