package responses

import (
<<<<<<< HEAD:pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go
=======
	"bytes"
	"encoding/json"
>>>>>>> upstream/main:internal/translator/openai/openai/responses/openai_openai-responses_request_test.go
	"testing"

	"github.com/tidwall/gjson"
)

<<<<<<< HEAD:pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go
func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"instructions": "Be helpful.",
=======
func prettyJSONForTest(raw []byte) string {
	if !gjson.ValidBytes(raw) {
		return string(raw)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return string(raw)
	}
	return out.String()
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergeConsecutiveFunctionCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"exec_command:0","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call","call_id":"exec_command:1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"exec_command:0","output":"ok0"},
			{"type":"function_call_output","call_id":"exec_command:1","output":"ok1"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	msgs := gjson.GetBytes(out, "messages")
	if !msgs.Exists() || !msgs.IsArray() {
		t.Fatalf("messages should be an array")
	}
	if got := len(msgs.Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}

	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := len(gjson.GetBytes(out, "messages.0.tool_calls").Array()); got != 2 {
		t.Fatalf("messages.0.tool_calls length = %d, want %d", got, 2)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "exec_command:0" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.1.id").String(); got != "exec_command:1" {
		t.Fatalf("messages.0.tool_calls.1.id = %q, want %q", got, "exec_command:1")
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "exec_command:0" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "exec_command:0")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_call_id").String(); got != "exec_command:1" {
		t.Fatalf("messages.2.tool_call_id = %q, want %q", got, "exec_command:1")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SplitFunctionCallsWhenInterrupted(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"next"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 3 {
		t.Fatalf("messages count = %d, want %d", got, 3)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want %q", got, "call_a")
	}
	if got := gjson.GetBytes(out, "messages.2.tool_calls.0.id").String(); got != "call_b" {
		t.Fatalf("messages.2.tool_calls.0.id = %q, want %q", got, "call_b")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilToolOutput(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_x","name":"exec_command","arguments":"{\"cmd\":\"echo hi\"}"},
			{"type":"message","role":"user","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_x","output":"ok"},
			{"type":"message","role":"user","content":"next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k2.6", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := len(gjson.GetBytes(out, "messages").Array()); got != 4 {
		t.Fatalf("messages count = %d, want %d", got, 4)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want %q", got, "assistant")
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want %q", got, "tool")
	}
	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_x" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_x")
	}
	if got := gjson.GetBytes(out, "messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want %q", got, "user")
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "Approved command prefix saved" {
		t.Fatalf("messages.2.content = %q, want %q", got, "Approved command prefix saved")
	}
	if got := gjson.GetBytes(out, "messages.3.content").String(); got != "next" {
		t.Fatalf("messages.3.content = %q, want %q", got, "next")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToAssistantMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_1",
				"summary": [
					{"type": "summary_text", "text": "first line\n"},
					{"type": "summary_text", "text": "second line"}
				]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "answer"}]
			},
			{"type": "message", "role": "user", "content": "next"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "first line\nsecond line" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q; output=%s", got, "first line\nsecond line", out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.text").String(); got != "answer" {
		t.Fatalf("messages.0.content.0.text = %q, want answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AttachesReasoningToToolCallMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_tool",
				"summary": [{"type": "summary_text", "text": "tool reasoning"}]
			},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, true)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "tool reasoning" {
		t.Fatalf("messages.0.reasoning_content = %q, want tool reasoning; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.tool_calls.0.id").String(); got != "call_1" {
		t.Fatalf("messages.0.tool_calls.0.id = %q, want call_1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want tool; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsReasoningBeforeUserMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type": "reasoning", "id": "rs_empty", "summary": []},
			{"type": "message", "role": "user", "content": "continue"}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.#").Int(); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want placeholder; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.1.role").String(); got != "user" {
		t.Fatalf("messages.1.role = %q, want user; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceTools(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Use add_numbers."}
		],
		"tools": [
			{
				"type": "namespace",
				"name": "mcp__test_mcp__",
				"description": "Tools in the mcp__test_mcp__ namespace.",
				"tools": [
					{
						"type": "function",
						"name": "add_numbers",
						"description": "Add two numbers",
						"parameters": {
							"type": "object",
							"properties": {
								"a": { "type": "number" },
								"b": { "type": "number" }
							},
							"required": ["a", "b"]
						}
					}
				]
			}
		],
		"tool_choice": "auto"
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools.0.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "mcp__test_mcp__add_numbers" {
		t.Fatalf("tools.0.function.name = %q, want mcp__test_mcp__add_numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Add two numbers" {
		t.Fatalf("tools.0.function.description = %q, want Add two numbers; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "a" {
		t.Fatalf("tools.0.function.parameters.required.0 = %q, want a; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesStructuredToolChoice(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Run command."}
		],
		"tool_choice": {
			"type": "function",
			"function": {
				"name": "run_command"
			}
		}
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "run_command" {
		t.Fatalf("tool_choice.function.name = %q, want run_command; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesInputImageDetail(t *testing.T) {
	raw := []byte(`{
>>>>>>> upstream/main:internal/translator/openai/openai/responses/openai_openai-responses_request_test.go
		"input": [
			{
				"role": "user",
				"content": [
<<<<<<< HEAD:pkg/llmproxy/translator/openai/openai/responses/openai_openai-responses_request_test.go
					{"type": "input_text", "text": "hello"}
				]
			}
		],
		"max_output_tokens": 100
	}`)

	got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o-new", input, true)
	res := gjson.ParseBytes(got)

	if res.Get("model").String() != "gpt-4o-new" {
		t.Errorf("expected model gpt-4o-new, got %s", res.Get("model").String())
	}

	if res.Get("stream").Bool() != true {
		t.Errorf("expected stream true, got %v", res.Get("stream").Bool())
	}

	if res.Get("max_completion_tokens").Int() != 100 {
		t.Errorf("expected max_completion_tokens 100, got %d", res.Get("max_completion_tokens").Int())
	}
	if res.Get("max_tokens").Exists() {
		t.Errorf("max_tokens must not be present for OpenAI chat completions: %s", res.Get("max_tokens").Raw)
	}

	messages := res.Get("messages").Array()
	if len(messages) != 2 {
		t.Errorf("expected 2 messages (system + user), got %d", len(messages))
	}

	if messages[0].Get("role").String() != "system" || messages[0].Get("content").String() != "Be helpful." {
		t.Errorf("unexpected system message: %s", messages[0].Raw)
	}

	if messages[1].Get("role").String() != "user" || messages[1].Get("content.0.text").String() != "hello" {
		t.Errorf("unexpected user message: %s", messages[1].Raw)
	}

	// Test full input with messages, function calls, and results
	input2 := []byte(`{
		"instructions": "sys",
		"input": [
			{"role": "user", "content": "hello"},
			{"type": "function_call", "call_id": "c1", "name": "f1", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": "ok"}
		],
		"tools": [{"type": "function", "name": "f1", "description": "d1", "parameters": {"type": "object"}}],
		"max_output_tokens": 100,
		"reasoning": {"effort": "high"}
	}`)

	got2 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("m1", input2, false)
	res2 := gjson.ParseBytes(got2)

	if res2.Get("max_completion_tokens").Int() != 100 {
		t.Errorf("expected max_completion_tokens 100, got %d", res2.Get("max_completion_tokens").Int())
	}
	if res2.Get("max_tokens").Exists() {
		t.Errorf("max_tokens must not be present for OpenAI chat completions: %s", res2.Get("max_tokens").Raw)
	}

	if res2.Get("reasoning_effort").String() != "high" {
		t.Errorf("expected reasoning_effort high, got %s", res2.Get("reasoning_effort").String())
	}

	messages2 := res2.Get("messages").Array()
	// sys + user + assistant(tool_call) + tool(result)
	if len(messages2) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(messages2))
	}

	if messages2[2].Get("role").String() != "assistant" || !messages2[2].Get("tool_calls").Exists() {
		t.Error("expected third message to be assistant with tool_calls")
	}

	if messages2[3].Get("role").String() != "tool" || messages2[3].Get("content").String() != "ok" {
		t.Error("expected fourth message to be tool with content ok")
	}

	if len(res2.Get("tools").Array()) != 1 {
		t.Errorf("expected 1 tool, got %d", len(res2.Get("tools").Array()))
	}

	// Test with developer role, image, and parallel tool calls
	input3 := []byte(`{
		"model": "gpt-4o",
		"input": [
			{"role": "developer", "content": "dev msg"},
			{"role": "user", "content": [{"type": "input_image", "image_url": "http://img"}]}
		],
		"parallel_tool_calls": true
	}`)
	got3 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", input3, false)
	res3 := gjson.ParseBytes(got3)

	messages3 := res3.Get("messages").Array()
	if len(messages3) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages3))
	}
	// developer -> user
	if messages3[0].Get("role").String() != "user" {
		t.Errorf("expected developer role converted to user, got %s", messages3[0].Get("role").String())
	}
	// image content
	if messages3[1].Get("content.0.type").String() != "image_url" {
		t.Errorf("expected image_url type, got %s", messages3[1].Get("content.0.type").String())
	}
	if res3.Get("parallel_tool_calls").Bool() != true {
		t.Error("expected parallel_tool_calls true")
	}

	// Test input as string
	input4 := []byte(`{"input": "hello"}`)
	got4 := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", input4, false)
	res4 := gjson.ParseBytes(got4)
	if res4.Get("messages.0.content").String() != "hello" {
		t.Errorf("expected content hello, got %s", res4.Get("messages.0.content").String())
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsToolChoice(t *testing.T) {
	input := []byte(`{
		"model": "gpt-4o",
		"input": [{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}],
		"tool_choice": {"type":"function","function":{"name":"weather"}}
	}`)

	got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", input, false)
	res := gjson.ParseBytes(got)

	toolChoice := res.Get("tool_choice")
	if !toolChoice.Exists() {
		t.Fatalf("expected tool_choice")
	}
	if toolChoice.Get("type").String() != "function" {
		t.Fatalf("tool_choice.type = %s, want function", toolChoice.Get("type").String())
	}
	if toolChoice.Get("function.name").String() != "weather" {
		t.Fatalf("tool_choice.function.name = %s, want weather", toolChoice.Get("function.name").String())
	}

	if res.Get("tool_choice").Type != gjson.JSON {
		t.Fatalf("tool_choice should be object, got %s", res.Get("tool_choice").Type.String())
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsLegacyReasoningEffort(t *testing.T) {
	input := []byte(`{
		"model":"gpt-4.1",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],
		"reasoning.effort":"low"
	}`)

	output := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", input, false)
	if got := gjson.GetBytes(output, "reasoning_effort").String(); got != "low" {
		t.Fatalf("expected reasoning_effort low from legacy flat field, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsVariantFallback(t *testing.T) {
	input := []byte(`{
		"model":"gpt-4.1",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"ping"}]}],
		"variant":"medium"
	}`)

	output := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4.1", input, false)
	if got := gjson.GetBytes(output, "reasoning_effort").String(); got != "medium" {
		t.Fatalf("expected reasoning_effort medium from variant, got %q", got)
=======
					{
						"type": "input_image",
						"image_url": "https://example.com/image.png",
						"detail": "high"
					}
				]
			}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "https://example.com/image.png" {
		t.Fatalf("messages.0.content.0.image_url.url = %q, want https://example.com/image.png; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.image_url.detail").String(); got != "high" {
		t.Fatalf("messages.0.content.0.image_url.detail = %q, want high; output=%s", got, out)
>>>>>>> upstream/main:internal/translator/openai/openai/responses/openai_openai-responses_request_test.go
	}
}
