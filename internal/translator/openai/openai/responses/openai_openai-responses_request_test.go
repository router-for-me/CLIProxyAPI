package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_UnwrapsStringifiedToolOutputImages(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		imageIndex   int
		expectedURL  string
		expectedText string
		detail       string
	}{
		{
			name:         "Codex input image",
			output:       `[{"type":"input_text","text":"Captured screenshot."},{"detail":"original","image_url":"data:image/png;base64,AA==","type":"input_image"}]`,
			imageIndex:   1,
			expectedURL:  "data:image/png;base64,AA==",
			expectedText: "Captured screenshot.",
			detail:       "high",
		},
		{
			name:        "OpenAI image URL",
			output:      `[{"type":"image_url","image_url":{"url":"https://example.com/generated.png","detail":"high"}}]`,
			imageIndex:  0,
			expectedURL: "https://example.com/generated.png",
			detail:      "high",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"input": [
					{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
					{"type":"function_call_output","call_id":"call_image","output":%q}
				]
			}`, tt.output))

			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, false)
			content := gjson.GetBytes(out, "messages.1.content")
			if !content.IsArray() {
				t.Fatalf("expected tool content array, got %s; output=%s", content.Raw, out)
			}
			parts := content.Array()
			if len(parts) <= tt.imageIndex {
				t.Fatalf("expected image part at index %d, got %s", tt.imageIndex, content.Raw)
			}
			imagePart := parts[tt.imageIndex]
			if got := imagePart.Get("type").String(); got != "image_url" {
				t.Fatalf("image type = %q, want image_url; part=%s", got, imagePart.Raw)
			}
			if got := imagePart.Get("image_url.url").String(); got != tt.expectedURL {
				t.Fatalf("image URL = %q, want %q; part=%s", got, tt.expectedURL, imagePart.Raw)
			}
			if got := imagePart.Get("image_url.detail").String(); got != tt.detail {
				t.Fatalf("image detail = %q, want %q; part=%s", got, tt.detail, imagePart.Raw)
			}
			if tt.expectedText != "" {
				if got := parts[0].Get("type").String(); got != "text" {
					t.Fatalf("text type = %q, want text; part=%s", got, parts[0].Raw)
				}
				if got := parts[0].Get("text").String(); got != tt.expectedText {
					t.Fatalf("text = %q, want %q; part=%s", got, tt.expectedText, parts[0].Raw)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsStructuredToolOutputImages(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_image","name":"view_image","arguments":"{}"},
			{
				"type":"function_call_output",
				"call_id":"call_image",
				"output":[
					{"type":"input_text","text":"Captured screenshot."},
					{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":"original"}
				]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, false)
	content := gjson.GetBytes(out, "messages.1.content")
	if !content.IsArray() {
		t.Fatalf("expected tool content array, got %s; output=%s", content.Raw, out)
	}
	if got := content.Get("1.type").String(); got != "image_url" {
		t.Fatalf("image type = %q, want image_url; output=%s", got, out)
	}
	if got := content.Get("1.image_url.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("image URL = %q, want data URL; output=%s", got, out)
	}
	if got := content.Get("1.image_url.detail").String(); got != "high" {
		t.Fatalf("image detail = %q, want high; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsNonImageToolOutputStrings(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{name: "plain text", output: "plain output"},
		{name: "JSON object", output: `{"status":"ok"}`},
		{name: "text-only array", output: `[{"type":"input_text","text":"still text"}]`},
		{name: "invalid image array", output: `[{"type":"input_image","detail":"low"}]`},
		{name: "image array with trailing text", output: `[{"type":"input_image","image_url":"data:image/png;base64,AA=="}] trailing`},
		{name: "truncated image array", output: `[{"type":"input_image","image_url":"data:image/png;base64,AA=="}`},
		{name: "non-string image URL", output: `[{"type":"input_image","image_url":123}]`},
		{name: "non-string image detail", output: `[{"type":"input_image","image_url":"data:image/png;base64,AA==","detail":123}]`},
		{name: "non-string text in image array", output: `[{"type":"input_text","text":123},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"input": [
					{"type":"function_call","call_id":"call_output","name":"inspect","arguments":"{}"},
					{"type":"function_call_output","call_id":"call_output","output":%q}
				]
			}`, tt.output))

			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("k3", raw, false)
			content := gjson.GetBytes(out, "messages.1.content")
			if content.Type != gjson.String {
				t.Fatalf("expected tool content string, got %s; output=%s", content.Raw, out)
			}
			if got := content.String(); got != tt.output {
				t.Fatalf("tool content = %q, want %q; output=%s", got, tt.output, out)
			}
		})
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_QualifiesNamespaceFunctionCallHistory(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_get_me","name":"get_me","namespace":"mcp__github","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_get_me","output":"ok"}
		],
		"tools": [
			{
				"type":"namespace",
				"name":"mcp__github",
				"tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	gotHistoryName := gjson.GetBytes(out, "messages.0.tool_calls.0.function.name").String()
	gotDeclaredName := gjson.GetBytes(out, "tools.0.function.name").String()
	if gotHistoryName != "mcp__github__get_me" {
		t.Fatalf("history function name = %q, want mcp__github__get_me; output=%s", gotHistoryName, out)
	}
	if gotHistoryName != gotDeclaredName {
		t.Fatalf("history function name = %q, declared function name = %q; output=%s", gotHistoryName, gotDeclaredName, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FlattensNamespaceCustomTools(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "top-level tools",
			raw: []byte(`{
				"tools":[{
					"type":"namespace",
					"name":"terminal",
					"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
				}]
			}`),
		},
		{
			name: "additional tools",
			raw: []byte(`{
				"input":[{
					"type":"additional_tools",
					"tools":[{
						"type":"namespace",
						"name":"terminal",
						"tools":[{"type":"custom","name":"exec","description":"Run a command"}]
					}]
				}]
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", tt.raw, false)

			if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
				t.Fatalf("tools count = %d, want 1; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "terminal__exec" {
				t.Fatalf("tool name = %q, want terminal__exec; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "Run a command" {
				t.Fatalf("tool description = %q, want Run a command; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.type").String(); got != "object" {
				t.Fatalf("parameters type = %q, want object; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.input.type").String(); got != "string" {
				t.Fatalf("input type = %q, want string; output=%s", got, out)
			}
			if got := gjson.GetBytes(out, "tools.0.function.parameters.required.0").String(); got != "input" {
				t.Fatalf("required parameter = %q, want input; output=%s", got, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesStructuredToolChoice(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Run command."}
		],
		"tools": [
			{
				"type": "function",
				"name": "run_command",
				"parameters": {"type": "object"}
			}
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OmitsToolSettingsWithoutTools(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "empty tools",
			raw: []byte(`{
				"input": [{"role":"user","content":"say ok"}],
				"tools": [],
				"tool_choice": "auto",
				"parallel_tool_calls": false
			}`),
		},
		{
			name: "unconvertible tools",
			raw: []byte(`{
				"tools": [{"type":"unsupported"}],
				"tool_choice": "auto",
				"parallel_tool_calls": false
			}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("grok-4.5", tt.raw, false)

			for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
				if got := gjson.GetBytes(out, field); got.Exists() {
					t.Fatalf("%s should be omitted without tools; output=%s", field, out)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesParallelToolCallsWithTools(t *testing.T) {
	raw := []byte(`{
		"tools": [
			{
				"type": "function",
				"name": "run_command",
				"parameters": {"type": "object"}
			}
		],
		"parallel_tool_calls": false
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("grok-4.5", raw, false)

	if got := gjson.GetBytes(out, "parallel_tool_calls"); !got.Exists() || got.Bool() {
		t.Fatalf("parallel_tool_calls = %v, want false; output=%s", got.Value(), out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesJSONSchemaTextFormat(t *testing.T) {
	raw := []byte(`{
		"text": {
			"format": {
				"type": "json_schema",
				"name": "answer",
				"description": "Structured answer",
				"strict": true,
				"schema": {
					"type": "object",
					"properties": {
						"ok": {"type": "boolean"}
					},
					"required": ["ok"],
					"additionalProperties": false
				}
			}
		}
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_schema" {
		t.Fatalf("response_format.type = %q, want json_schema; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.name").String(); got != "answer" {
		t.Fatalf("response_format.json_schema.name = %q, want answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.description").String(); got != "Structured answer" {
		t.Fatalf("response_format.json_schema.description = %q, want Structured answer; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.strict"); !got.Exists() || !got.Bool() {
		t.Fatalf("response_format.json_schema.strict = %v, want true; output=%s", got.Value(), out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.properties.ok.type").String(); got != "boolean" {
		t.Fatalf("response_format.json_schema.schema.properties.ok.type = %q, want boolean; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.required.0").String(); got != "ok" {
		t.Fatalf("response_format.json_schema.schema.required.0 = %q, want ok; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema.schema.additionalProperties"); !got.Exists() || got.Bool() {
		t.Fatalf("response_format.json_schema.schema.additionalProperties = %v, want false; output=%s", got.Value(), out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesJSONObjectTextFormat(t *testing.T) {
	raw := []byte(`{"text":{"format":{"type":"json_object"}}}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format.type").String(); got != "json_object" {
		t.Fatalf("response_format.type = %q, want json_object; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "response_format.json_schema"); got.Exists() {
		t.Fatalf("response_format.json_schema should be omitted; output=%s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OmitsResponseFormatWithoutTextFormat(t *testing.T) {
	raw := []byte(`{"input":"Return plain text."}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)

	if got := gjson.GetBytes(out, "response_format"); got.Exists() {
		t.Fatalf("response_format should be omitted, got %s; output=%s", got.Raw, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_NormalizesInputImageDetail(t *testing.T) {
	tests := []struct {
		name           string
		detailJSON     string
		expectedDetail string
	}{
		{name: "standard high", detailJSON: `"high"`, expectedDetail: "high"},
		{name: "Codex original", detailJSON: `"original"`, expectedDetail: "high"},
		{name: "unsupported value", detailJSON: `"medium"`},
		{name: "non-string value", detailJSON: `123`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"input": [
					{
						"role": "user",
						"content": [
							{
								"type": "input_image",
								"image_url": "https://example.com/image.png",
								"detail": %s
							}
						]
					}
				]
			}`, tt.detailJSON))

			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)
			if got := gjson.GetBytes(out, "messages.0.content.0.image_url.url").String(); got != "https://example.com/image.png" {
				t.Fatalf("image URL = %q, want https://example.com/image.png; output=%s", got, out)
			}
			detail := gjson.GetBytes(out, "messages.0.content.0.image_url.detail")
			if tt.expectedDetail == "" {
				if detail.Exists() {
					t.Fatalf("image detail should be omitted, got %q; output=%s", detail.String(), out)
				}
				return
			}
			if got := detail.String(); got != tt.expectedDetail {
				t.Fatalf("image detail = %q, want %q; output=%s", got, tt.expectedDetail, out)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DeduplicatesToolsAcrossAdditionalTools(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"What time is it?"},
			{
				"type":"additional_tools",
				"tools":[
					{"type":"function","name":"get_time","description":"copy from additional_tools","parameters":{"type":"object","properties":{"tz":{"type":"string"}}}}
				]
			}
		],
		"tools": [
			{"type":"function","name":"get_time","description":"authoritative top-level definition","parameters":{"type":"object","properties":{"timezone":{"type":"string"}}}}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "get_time" {
		t.Fatalf("tools.0.function.name = %q, want get_time; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.description").String(); got != "authoritative top-level definition" {
		t.Fatalf("tools.0.function.description = %q, want the top-level definition to win; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.parameters.properties.timezone.type").String(); got != "string" {
		t.Fatalf("tools.0.function.parameters should come from the top-level definition; output=%s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DeduplicatesNamespaceQualifiedCollision(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Patch the file."}
		],
		"tools": [
			{"type":"function","name":"editor__apply_patch","parameters":{"type":"object"}},
			{
				"type":"namespace",
				"name":"editor",
				"tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]
			}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	if got := gjson.GetBytes(out, "tools.#").Int(); got != 1 {
		t.Fatalf("tools count = %d, want 1; output=%s", got, out)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "editor__apply_patch" {
		t.Fatalf("tools.0.function.name = %q, want editor__apply_patch; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_KeepsDistinctToolsFromBothSources(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Do the thing."},
			{
				"type":"additional_tools",
				"tools":[
					{"type":"function","name":"get_date","parameters":{"type":"object"}},
					{"type":"function","name":"get_time","parameters":{"type":"object"}}
				]
			}
		],
		"tools": [
			{"type":"function","name":"get_time","parameters":{"type":"object"}},
			{"type":"function","name":"get_weather","parameters":{"type":"object"}}
		]
	}`)
	t.Logf("input json:\n%s", prettyJSONForTest(raw))

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", raw, false)
	t.Logf("output json:\n%s", prettyJSONForTest(out))

	want := []string{"get_time", "get_weather", "get_date"}
	if got := gjson.GetBytes(out, "tools.#").Int(); got != int64(len(want)) {
		t.Fatalf("tools count = %d, want %d; output=%s", got, len(want), out)
	}
	for i, wantName := range want {
		got := gjson.GetBytes(out, fmt.Sprintf("tools.%d.function.name", i)).String()
		if got != wantName {
			t.Fatalf("tools.%d.function.name = %q, want %q; output=%s", i, got, wantName, out)
		}
	}
}

func TestResponsesSingleCustomToolName_CountsDeduplicatedTools(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"Patch the file."},
			{
				"type":"additional_tools",
				"tools":[{"type":"custom","name":"apply_patch","description":"copy"}]
			}
		],
		"tools": [
			{"type":"custom","name":"apply_patch","description":"authoritative"}
		]
	}`)

	name, ok := responsesSingleCustomToolName(raw)
	if !ok {
		t.Fatalf("responsesSingleCustomToolName ok = false, want true when the only tool is duplicated across both sources")
	}
	if name != "apply_patch" {
		t.Fatalf("responsesSingleCustomToolName name = %q, want apply_patch", name)
	}
}

func TestSplitResponsesQualifiedFunctionCallFromRequest_FirstDeclarationWins(t *testing.T) {
	flatFirst := []byte(`{
		"tools": [
			{"type":"function","name":"editor__apply_patch","parameters":{"type":"object"}},
			{"type":"namespace","name":"editor","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}
		]
	}`)
	namespaceFirst := []byte(`{
		"tools": [
			{"type":"namespace","name":"editor","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]},
			{"type":"function","name":"editor__apply_patch","parameters":{"type":"object"}}
		]
	}`)
	namespaceOnly := []byte(`{
		"tools": [
			{"type":"namespace","name":"mcp__github","tools":[{"type":"function","name":"get_me","parameters":{"type":"object"}}]}
		]
	}`)

	tests := []struct {
		name          string
		raw           []byte
		qualified     string
		wantName      string
		wantNamespace string
	}{
		// The flat tool is the one that survives merging, so it must stay flat.
		{"flat declared first", flatFirst, "editor__apply_patch", "editor__apply_patch", ""},
		// The namespace child survives here, so the call splits back into it.
		{"namespace declared first", namespaceFirst, "editor__apply_patch", "apply_patch", "editor"},
		// No collision: unchanged behaviour.
		{"namespace only", namespaceOnly, "mcp__github__get_me", "get_me", "mcp__github"},
		// Unknown name falls through untouched.
		{"unknown name", flatFirst, "something_else", "something_else", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotNamespace := splitResponsesQualifiedFunctionCallFromRequest(tt.raw, tt.qualified)
			if gotName != tt.wantName || gotNamespace != tt.wantNamespace {
				t.Fatalf("split(%q) = (%q, %q), want (%q, %q)",
					tt.qualified, gotName, gotNamespace, tt.wantName, tt.wantNamespace)
			}
		})
	}
}

func TestSplitResponsesQualifiedFunctionCallFromRequest_MatchesMergedToolIdentity(t *testing.T) {
	// Whatever survives the merge must be what reverse translation reports.
	raw := []byte(`{
		"tools": [
			{"type":"function","name":"editor__apply_patch","parameters":{"type":"object"}},
			{"type":"namespace","name":"editor","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}
		]
	}`)

	merged := mergeResponsesRequestChatTools(gjson.ParseBytes(raw))
	if len(merged) != 1 {
		t.Fatalf("merged tool count = %d, want 1", len(merged))
	}
	emitted := gjson.GetBytes(merged[0], "function.name").String()

	name, namespace := splitResponsesQualifiedFunctionCallFromRequest(raw, emitted)
	if namespace != "" {
		t.Fatalf("emitted tool %q came from a flat declaration, but split reported namespace %q", emitted, namespace)
	}
	if name != emitted {
		t.Fatalf("split(%q) name = %q, want %q", emitted, name, emitted)
	}
}

func TestResponsesCustomToolNames_FollowsMergedDeclaration(t *testing.T) {
	// Declarations delivered through the two channels may differ in type: a
	// top-level function and an "additional_tools" custom tool can flatten to
	// the same Chat Completions name. Only the winner may decide whether the
	// tool is freeform, otherwise a plain function call comes back as a
	// custom_tool_call with unwrapped arguments.
	functionFirst := []byte(`{
		"input": [
			{"type":"additional_tools","tools":[{"type":"custom","name":"exec","description":"copy"}]}
		],
		"tools": [
			{"type":"function","name":"exec","parameters":{"type":"object"}}
		]
	}`)
	customFirst := []byte(`{
		"input": [
			{"type":"additional_tools","tools":[{"type":"function","name":"exec","parameters":{"type":"object"}}]}
		],
		"tools": [
			{"type":"custom","name":"exec","description":"authoritative"}
		]
	}`)

	tests := []struct {
		name       string
		raw        []byte
		wantCustom bool
	}{
		{name: "function declaration wins", raw: functionFirst, wantCustom: false},
		{name: "custom declaration wins", raw: customFirst, wantCustom: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := mergeResponsesRequestChatTools(gjson.ParseBytes(tt.raw))
			if len(merged) != 1 {
				t.Fatalf("merged tool count = %d, want 1", len(merged))
			}
			// Freeform tools are the ones converted to the single-string shape.
			mergedIsCustom := gjson.GetBytes(merged[0], "function.parameters.properties.input").Exists()
			if mergedIsCustom != tt.wantCustom {
				t.Fatalf("merged tool custom = %v, want %v", mergedIsCustom, tt.wantCustom)
			}

			if _, isCustom := responsesCustomToolNames(tt.raw)["exec"]; isCustom != tt.wantCustom {
				t.Fatalf("responsesCustomToolNames classified exec as custom = %v, want %v", isCustom, tt.wantCustom)
			}

			name, ok := responsesSingleCustomToolName(tt.raw)
			if ok != tt.wantCustom {
				t.Fatalf("responsesSingleCustomToolName ok = %v, want %v", ok, tt.wantCustom)
			}
			if ok && name != "exec" {
				t.Fatalf("responsesSingleCustomToolName name = %q, want exec", name)
			}
		})
	}
}

func TestResponsesCustomToolNames_OnlyReportsMergedTools(t *testing.T) {
	// Nested namespaces are not converted, so their children never reach the
	// upstream request and must not be classified as freeform tools either.
	raw := []byte(`{
		"tools": [
			{"type":"namespace","name":"outer","tools":[
				{"type":"namespace","name":"inner","tools":[{"type":"custom","name":"buried"}]},
				{"type":"custom","name":"reachable"}
			]}
		]
	}`)

	mergedNames := make(map[string]struct{})
	for _, chatTool := range mergeResponsesRequestChatTools(gjson.ParseBytes(raw)) {
		mergedNames[gjson.GetBytes(chatTool, "function.name").String()] = struct{}{}
	}
	if _, ok := mergedNames["outer__reachable"]; !ok {
		t.Fatalf("merged tool names = %v, want outer__reachable", mergedNames)
	}

	for name := range responsesCustomToolNames(raw) {
		if _, ok := mergedNames[name]; !ok {
			t.Fatalf("responsesCustomToolNames reported %q, which the merge never emits", name)
		}
	}
}
