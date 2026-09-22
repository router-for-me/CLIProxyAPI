package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_UnwrapsStringifiedCustomToolOutputImages(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"custom_tool_call","call_id":"call_image","name":"view_image","input":"{}"},
			{"type":"custom_tool_call_output","call_id":"call_image","output":"[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,AA==\",\"detail\":\"original\"}]"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)
	content := gjson.GetBytes(out, "messages.1.content")
	if !content.IsArray() {
		t.Fatalf("expected custom tool content array, got %s; output=%s", content.Raw, out)
	}
	if got := content.Get("0.type").String(); got != "image_url" {
		t.Fatalf("image type = %q, want image_url; output=%s", got, out)
	}
	if got := content.Get("0.image_url.url").String(); got != "data:image/png;base64,AA==" {
		t.Fatalf("image URL = %q, want data URL; output=%s", got, out)
	}
	if got := content.Get("0.image_url.detail").String(); got != "high" {
		t.Fatalf("image detail = %q, want high; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesCustomToolOutputFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{name: "plain text", output: `"plain output"`, expected: "plain output"},
		{name: "text content array", output: `[{"type":"input_text","text":"done"}]`, expected: "done"},
		{name: "invalid image array", output: `[{"type":"input_image","detail":"low"}]`, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{
				"input": [
					{"type":"custom_tool_call","call_id":"call_output","name":"inspect","input":"{}"},
					{"type":"custom_tool_call_output","call_id":"call_output","output":%s}
				]
			}`, tt.output))

			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)
			content := gjson.GetBytes(out, "messages.1.content")
			if content.Type != gjson.String {
				t.Fatalf("expected custom tool content string, got %s; output=%s", content.Raw, out)
			}
			if got := content.String(); got != tt.expected {
				t.Fatalf("custom tool content = %q, want %q; output=%s", got, tt.expected, out)
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesAssistantContentWithToolCalls(t *testing.T) {
	raw := []byte(`{
		"input": [
			{
				"type": "reasoning",
				"id": "rs_1",
				"summary": [{"type": "summary_text", "text": "inspect the next step"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Step 3 completed; continue to step 4."}]
			},
			{"type":"function_call","call_id":"call_4","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_4","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if got := len(messages); got != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", got, out)
	}
	assistant := messages[0]
	if got := assistant.Get("role").String(); got != "assistant" {
		t.Fatalf("assistant role = %q, want assistant; output=%s", got, out)
	}
	if got := assistant.Get("reasoning_content").String(); got != "inspect the next step" {
		t.Fatalf("assistant reasoning_content = %q, want inspect the next step; output=%s", got, out)
	}
	if got := assistant.Get("content.0.text").String(); got != "Step 3 completed; continue to step 4." {
		t.Fatalf("assistant content = %q, want preserved text; output=%s", got, out)
	}
	if got := assistant.Get("tool_calls.0.id").String(); got != "call_4" {
		t.Fatalf("assistant tool call ID = %q, want call_4; output=%s", got, out)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_4" {
		t.Fatalf("tool output call ID = %q, want call_4; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DoesNotMergeToolCallsAcrossUserMessage(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]},
			{"type":"function_call","call_id":"call_next","name":"exec_command","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_next","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if got := len(messages); got != 4 {
		t.Fatalf("messages count = %d, want 4; output=%s", got, out)
	}
	if messages[0].Get("tool_calls").Exists() {
		t.Fatalf("messages.0 unexpectedly contains tool calls; output=%s", out)
	}
	if got := messages[1].Get("role").String(); got != "user" {
		t.Fatalf("messages.1 role = %q, want user; output=%s", got, out)
	}
	if got := messages[2].Get("tool_calls.0.id").String(); got != "call_next" {
		t.Fatalf("messages.2 tool call ID = %q, want call_next; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MergesDistinctReasoningWithinAssistantTurn(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"reasoning","summary":[{"type":"summary_text","text":"first"}]},
			{"type":"message","role":"assistant","reasoning_content":"first","content":[{"type":"output_text","text":"working"}]},
			{"type":"reasoning","summary":[{"type":"summary_text","text":"second"}]},
			{"type":"function_call","call_id":"call_reasoning","name":"exec_command","arguments":"{}"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if got := len(messages); got != 1 {
		t.Fatalf("messages count = %d, want 1; output=%s", got, out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "first\n\nsecond" {
		t.Fatalf("reasoning_content = %q, want %q; output=%s", got, "first\n\nsecond", out)
	}
	if got := messages[0].Get("tool_calls.0.id").String(); got != "call_reasoning" {
		t.Fatalf("tool call ID = %q, want call_reasoning; output=%s", got, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ReplacesUnavailableReasoningWithinAssistantTurn(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"type":"reasoning","summary":[]},
			{"type":"message","role":"assistant","reasoning_content":"real reasoning","content":[{"type":"output_text","text":"working"}]},
			{"type":"function_call","call_id":"call_real_reasoning","name":"exec_command","arguments":"{}"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("kimi-k3", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if got := len(messages); got != 1 {
		t.Fatalf("messages count = %d, want 1; output=%s", got, out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "real reasoning" {
		t.Fatalf("reasoning_content = %q, want real reasoning; output=%s", got, out)
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesReasoningOnFollowUpToolTurns(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "first plan"}]},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "starting"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			{"type": "function_call", "call_id": "call_2", "name": "write_stdin", "arguments": "{\"data\":\"x\"}"},
			{"type": "function_call_output", "call_id": "call_2", "output": "ok"},
			{"type": "reasoning", "id": "rs_2", "summary": [{"type": "summary_text", "text": "second plan"}]},
			{"type": "function_call", "call_id": "call_3", "name": "exec_command", "arguments": "{\"cmd\":\"pwd\"}"},
			{"type": "function_call_output", "call_id": "call_3", "output": "ok"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "apply_patch is available"}]},
			{"type": "function_call", "call_id": "call_4", "name": "exec_command", "arguments": "{\"cmd\":\"cat\"}"},
			{"type": "function_call_output", "call_id": "call_4", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, true)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 8 {
		t.Fatalf("messages count = %d, want 8; output=%s", len(messages), out)
	}

	for i, msg := range messages {
		if msg.Get("role").String() == "assistant" && msg.Get("tool_calls").Exists() && len(msg.Get("tool_calls").Array()) > 0 {
			rc := msg.Get("reasoning_content").String()
			if rc == "" {
				t.Fatalf("messages[%d] with tool_calls is missing reasoning_content; message=%s", i, msg.Raw)
			}
		}
	}

	if got := messages[2].Get("tool_calls.0.id").String(); got != "call_2" {
		t.Fatalf("messages.2 tool call id = %q, want call_2; output=%s", got, out)
	}
	if got := messages[2].Get("reasoning_content").String(); got != "first plan" {
		t.Fatalf("messages.2 reasoning_content = %q, want %q; output=%s", got, "first plan", out)
	}
	if got := messages[6].Get("tool_calls.0.id").String(); got != "call_4" {
		t.Fatalf("messages.6 tool call id = %q, want call_4; output=%s", got, out)
	}
	if got := messages[6].Get("reasoning_content").String(); got != "second plan" {
		t.Fatalf("messages.6 reasoning_content = %q, want %q; output=%s", got, "second plan", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FallsBackToPlaceholderWhenNoPriorReasoning(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "[reasoning unavailable]", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_EffortNoneDoesNotInjectReasoning(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-4o",
		"reasoning": {"effort": "none"},
		"input": [
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-4o", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("messages count = %d, want 2; output=%s", len(messages), out)
	}
	if messages[0].Get("reasoning_content").Exists() {
		t.Fatalf("messages.0 should not have reasoning_content when effort is none; output=%s", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesReasoningOnCustomToolCallTurns(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "custom plan"}]},
			{"type": "custom_tool_call", "call_id": "cust_1", "name": "do_work", "input": "step1"},
			{"type": "custom_tool_call_output", "call_id": "cust_1", "output": "done"},
			{"type": "custom_tool_call", "call_id": "cust_2", "name": "do_work", "input": "step2"},
			{"type": "custom_tool_call_output", "call_id": "cust_2", "output": "done"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages count = %d, want 4; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "custom plan" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "custom plan", out)
	}
	if got := messages[2].Get("reasoning_content").String(); got != "custom plan" {
		t.Fatalf("messages.2 reasoning_content = %q, want %q; output=%s", got, "custom plan", out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ResetsReasoningAcrossUserMessageBoundary(t *testing.T) {
	raw := []byte(`{
		"model": "deepseek-v4.1-flash",
		"reasoning": {"effort": "high"},
		"input": [
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "turn 1 plan"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "ok"},
			{"type": "message", "role": "user", "content": "now do step 2"},
			{"type": "function_call", "call_id": "call_2", "name": "exec_command", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "call_2", "output": "ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", raw, false)

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("messages count = %d, want 5; output=%s", len(messages), out)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "turn 1 plan" {
		t.Fatalf("messages.0 reasoning_content = %q, want %q; output=%s", got, "turn 1 plan", out)
	}
	if got := messages[3].Get("reasoning_content").String(); got != "[reasoning unavailable]" {
		t.Fatalf("messages.3 reasoning_content = %q, want %q; output=%s", got, "[reasoning unavailable]", out)
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsCanonicalResponsesNamedToolChoice(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.4",
		"input": [{"role": "user", "content": "Call gateway_echo with value TOOL_OK."}],
		"tools": [{
			"type": "function",
			"name": "gateway_echo",
			"description": "Returns the given value",
			"parameters": {
				"type": "object",
				"properties": {"value": {"type": "string"}},
				"required": ["value"],
				"additionalProperties": false
			}
		}],
		"tool_choice": {"type": "function", "name": "gateway_echo"},
		"max_output_tokens": 512
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)

	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.function.name").String(); got != "gateway_echo" {
		t.Fatalf("tool_choice.function.name = %q, want gateway_echo; output=%s", got, string(out))
	}
	if gjson.GetBytes(out, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ConvertsNamespaceAndCustomToolChoice(t *testing.T) {
	rawNamespace := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "namespace",
				"name": "service_tools",
				"tools": [
					{
						"type": "function",
						"name": "lookup",
						"parameters": {"type": "object"}
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "lookup"
		}
	}`)

	outNamespace := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawNamespace, false)
	if got := gjson.GetBytes(outNamespace, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(outNamespace))
	}
	if got := gjson.GetBytes(outNamespace, "tool_choice.function.name").String(); got != "service_tools__lookup" {
		t.Fatalf("tool_choice.function.name = %q, want service_tools__lookup; output=%s", got, string(outNamespace))
	}
	if declaredToolName := gjson.GetBytes(outNamespace, "tools.0.function.name").String(); declaredToolName != gjson.GetBytes(outNamespace, "tool_choice.function.name").String() {
		t.Fatalf("tool_choice.function.name (%q) must match declared tools.0.function.name (%q); output=%s", gjson.GetBytes(outNamespace, "tool_choice.function.name").String(), declaredToolName, string(outNamespace))
	}
	if gjson.GetBytes(outNamespace, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(outNamespace))
	}

	rawExplicitNamespace := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "namespace",
				"name": "service_tools",
				"tools": [
					{
						"type": "function",
						"name": "lookup",
						"parameters": {"type": "object"}
					}
				]
			}
		],
		"tool_choice": {
			"type": "function",
			"name": "lookup",
			"namespace": "service_tools"
		}
	}`)

	outExplicitNamespace := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawExplicitNamespace, false)
	if got := gjson.GetBytes(outExplicitNamespace, "tool_choice.function.name").String(); got != "service_tools__lookup" {
		t.Fatalf("explicit namespace tool_choice.function.name = %q, want service_tools__lookup; output=%s", got, string(outExplicitNamespace))
	}
	if gjson.GetBytes(outExplicitNamespace, "tool_choice.namespace").Exists() {
		t.Fatalf("tool_choice.namespace should be absent at top-level; output=%s", string(outExplicitNamespace))
	}
	if gjson.GetBytes(outExplicitNamespace, "tool_choice.name").Exists() {
		t.Fatalf("tool_choice.name should be absent at top-level; output=%s", string(outExplicitNamespace))
	}

	rawCustom := []byte(`{
		"model": "gpt-5.4",
		"input": "test",
		"tools": [
			{
				"type": "custom",
				"name": "patch_runner",
				"description": "Applies diff"
			}
		],
		"tool_choice": {
			"type": "custom",
			"name": "patch_runner"
		}
	}`)

	outCustom := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawCustom, false)
	if got := gjson.GetBytes(outCustom, "tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function; output=%s", got, string(outCustom))
	}
	if got := gjson.GetBytes(outCustom, "tool_choice.function.name").String(); got != "patch_runner" {
		t.Fatalf("tool_choice.function.name = %q, want patch_runner; output=%s", got, string(outCustom))
	}

	for _, scalar := range []string{`"auto"`, `"none"`, `"required"`} {
		rawScalar := []byte(`{
			"model": "gpt-5.4",
			"input": "test",
			"tools": [
				{
					"type": "function",
					"name": "lookup",
					"parameters": {"type": "object"}
				}
			],
			"tool_choice": ` + scalar + `
		}`)
		outScalar := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawScalar, false)
		if got := gjson.GetBytes(outScalar, "tool_choice").Raw; got != scalar {
			t.Fatalf("tool_choice = %q, want %s; output=%s", got, scalar, string(outScalar))
		}
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

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_FunctionCallOutputAlternateIDsAndQueueFallback(t *testing.T) {
	testCases := []struct {
		name           string
		outputField    string
		wantToolCallID string
	}{
		{
			name:           "call_id standard",
			outputField:    `"call_id":"call_123"`,
			wantToolCallID: "call_123",
		},
		{
			name:           "tool_call_id alternate field",
			outputField:    `"tool_call_id":"call_123"`,
			wantToolCallID: "call_123",
		},
		{
			name:           "callId alternate field",
			outputField:    `"callId":"call_123"`,
			wantToolCallID: "call_123",
		},
		{
			name:           "id alternate field",
			outputField:    `"id":"call_123"`,
			wantToolCallID: "call_123",
		},
		{
			name:           "missing call_id completely fallback to pending queue",
			outputField:    ``,
			wantToolCallID: "call_123",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			outputJSON := `{"type":"function_call_output","output":"tool_result_ok"`
			if tc.outputField != "" {
				outputJSON += `,` + tc.outputField
			}
			outputJSON += `}`

			inputJSON := []byte(`{
				"model": "deepseek-v4-flash",
				"input": [
					{"type":"function_call","call_id":"call_123","name":"Bash","arguments":"{\"command\":\"ls\"}"},
					` + outputJSON + `
				]
			}`)

			out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", inputJSON, false)
			messages := gjson.GetBytes(out, "messages").Array()
			if len(messages) != 2 {
				t.Fatalf("expected 2 messages (assistant, tool), got %d; output=%s", len(messages), string(out))
			}

			// Assistant message has tool_calls with id call_123
			toolCallID := messages[0].Get("tool_calls.0.id").String()
			if toolCallID != "call_123" {
				t.Fatalf("tool_calls.0.id = %q, want call_123", toolCallID)
			}

			// Tool message has tool_call_id matching call_123
			toolMessage := messages[1]
			if toolMessage.Get("role").String() != "tool" {
				t.Fatalf("expected role tool, got %s", toolMessage.Raw)
			}
			if gotID := toolMessage.Get("tool_call_id").String(); gotID != tc.wantToolCallID {
				t.Fatalf("tool_call_id = %q, want %q; output=%s", gotID, tc.wantToolCallID, string(out))
			}
			if gotContent := toolMessage.Get("content").String(); gotContent != "tool_result_ok" {
				t.Fatalf("tool message content = %q, want tool_result_ok", gotContent)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MixedMissingAndExplicitParallelOutputs(t *testing.T) {
	// Call A, Call B.
	// Output 1 has NO ID (result B).
	// Output 2 explicitly has call_id: call_a (result A).
	// Call A must NOT be stolen by Output 1; Output 1 must get Call B.
	inputJSON := []byte(`{
		"model": "deepseek-v4-flash",
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages (assistant, tool_b, tool_a), got %d; output=%s", len(messages), string(out))
	}

	resultMap := make(map[string]string)
	for _, m := range messages[1:] {
		resultMap[m.Get("tool_call_id").String()] = m.Get("content").String()
	}

	if got := resultMap["call_a"]; got != "result_a" {
		t.Fatalf("result for call_a = %q, want result_a", got)
	}
	if got := resultMap["call_b"]; got != "result_b" {
		t.Fatalf("result for call_b = %q, want result_b", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DefersMessageUntilMissingIDToolOutput(t *testing.T) {
	// Call A -> intervening user message -> Output with missing call_id
	// The user message must be deferred until AFTER the tool output!
	inputJSON := []byte(`{
		"model": "deepseek-v4-flash",
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"message","role":"user","content":"User command while running"},
			{"type":"function_call_output","output":"result_a"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages (assistant, tool, user), got %d; output=%s", len(messages), string(out))
	}

	// Message 0: assistant with tool_call
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("messages[0].role = %q, want assistant", got)
	}
	// Message 1: tool response for call_a (strictly adjacent to assistant tool_calls!)
	if got := messages[1].Get("role").String(); got != "tool" {
		t.Fatalf("messages[1].role = %q, want tool (user message was not deferred!)", got)
	}
	if got := messages[1].Get("tool_call_id").String(); got != "call_a" {
		t.Fatalf("messages[1].tool_call_id = %q, want call_a", got)
	}
	// Message 2: deferred user message
	if got := messages[2].Get("role").String(); got != "user" {
		t.Fatalf("messages[2].role = %q, want user", got)
	}
	if got := messages[2].Get("content").String(); got != "User command while running" {
		t.Fatalf("messages[2].content = %q, want 'User command while running'", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MixedMissingAndExplicitParallelOutputsAcrossUserMessage(t *testing.T) {
	// Call A, Call B.
	// Output 1 has NO ID (result B).
	// Intervening user message.
	// Output 2 explicitly has call_id: call_a (result A).
	// Call A must NOT be stolen by Output 1; Output 1 must get Call B.
	inputJSON := []byte(`{
		"model": "deepseek-v4-flash",
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
			{"type":"function_call_output","output":"result_b"},
			{"type":"message","role":"user","content":"status?"},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	resultMap := make(map[string]string)
	for _, m := range messages {
		if m.Get("role").String() == "tool" {
			resultMap[m.Get("tool_call_id").String()] = m.Get("content").String()
		}
	}

	if got := resultMap["call_a"]; got != "result_a" {
		t.Fatalf("result for call_a = %q, want result_a", got)
	}
	if got := resultMap["call_b"]; got != "result_b" {
		t.Fatalf("result for call_b = %q, want result_b", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OrphanFunctionCallOutputBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","id":"fco_01a09fca-8d33-73a1-97fd-4d83ecc02f9d","name":"send_message_to_thread","output":"<codex_delegation>\n  <source_thread_id>01a022d7-d4d0-72b2-8571-4590484ccaee</source_thread_id>\n  <input>Execute sub-task</input>\n</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","id":"fco_01a09fca-a5f0-7b40-9943-21fbc923c537","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && strings.TrimSpace(message.Get("tool_call_id").String()) == "" {
			t.Fatalf("orphan output emitted as tool message with empty tool_call_id: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
			if got := message.Get("content").String(); got != "/Users/developer" {
				t.Fatalf("bash tool content = %q, want /Users/developer; output=%s", got, string(out))
			}
		}
	}
	if !delegationFound {
		t.Fatalf("expected orphan send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_UnpairedExplicitCallIDBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","call_id":"call_missing","name":"send_message_to_thread","output":"<codex_delegation>Execute sub-task</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && message.Get("tool_call_id").String() == "call_missing" {
			t.Fatalf("unpaired output emitted as tool message: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
		}
	}
	if !delegationFound {
		t.Fatalf("expected unpaired send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_CapsLongNamespaceToolNames(t *testing.T) {
	raw := []byte(`{
		"input": [
			{"role":"user","content":"hi"}
		],
		"tools": [
			{"type":"function","name":"exec_command","parameters":{"type":"object"}},
			{
				"type":"namespace",
				"name":"mcp__codex_apps__codex_document_control",
				"tools":[
					{"type":"function","name":"_execute_document_command","parameters":{"type":"object"}},
					{"type":"function","name":"_get_document_tool_schemas","parameters":{"type":"object"}}
				]
			},
			{
				"type":"namespace",
				"name":"mcp__codex_apps__safety_settings",
				"tools":[
					{"type":"function","name":"_prepare_parental_control_update","parameters":{"type":"object"}}
				]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", raw, false)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 4 {
		t.Fatalf("tools count = %d, want 4; output=%s", len(tools), out)
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		name := tool.Get("function.name").String()
		if len(name) > 64 {
			t.Errorf("function.name %q (len %d) exceeds the 64-character limit; output=%s", name, len(name), out)
		}
		if seen[name] {
			t.Errorf("duplicate function.name %q after flattening; output=%s", name, out)
		}
		seen[name] = true
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DisambiguatesTruncationCollisions(t *testing.T) {
	// Two distinct namespace tools whose qualified names both truncate to the
	// same 64-char tail must survive as two usable chat tools, not be merged
	// or silently dropped by the first-wins deduplication.
	filler := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	raw := []byte(`{
		"input": [
			{"role":"user","content":"hi"}
		],
		"tools": [
			{
				"type":"namespace",
				"name":"mcp__server_one__` + filler + `",
				"tools":[{"type":"function","name":"_same_tail_tool_name","parameters":{"type":"object"}}]
			},
			{
				"type":"namespace",
				"name":"mcp__server_two__` + filler + `",
				"tools":[{"type":"function","name":"_same_tail_tool_name","parameters":{"type":"object"}}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", raw, false)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(tools), out)
	}
	first := tools[0].Get("function.name").String()
	second := tools[1].Get("function.name").String()
	if first == second {
		t.Fatalf("truncation collision was not disambiguated: both tools are %q; output=%s", first, out)
	}
	for _, name := range []string{first, second} {
		if len(name) > 64 {
			t.Errorf("disambiguated name %q (len %d) exceeds 64; output=%s", name, len(name), out)
		}
	}

	// A replayed call to the renamed declaration must resolve to the renamed
	// chat name so the assistant history matches the tools array.
	merged := []byte(`{
		"input": [
			{"type":"custom_tool_call","namespace":"mcp__server_two__` + filler + `","name":"_same_tail_tool_name","call_id":"call_1","input":"x"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"y"}
		],
		"tools": [
			{
				"type":"namespace",
				"name":"mcp__server_one__` + filler + `",
				"tools":[{"type":"function","name":"_same_tail_tool_name","parameters":{"type":"object"}}]
			},
			{
				"type":"namespace",
				"name":"mcp__server_two__` + filler + `",
				"tools":[{"type":"function","name":"_same_tail_tool_name","parameters":{"type":"object"}}]
			}
		]
	}`)
	replayOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", merged, false)
	replayedName := ""
	for _, m := range gjson.GetBytes(replayOut, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			replayedName = m.Get("tool_calls.0.function.name").String()
		}
	}
	if replayedName != second {
		t.Fatalf("replayed collision-suffixed call name = %q, want %q to match the tools array; output=%s", replayedName, second, replayOut)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_LongDeclarationDoesNotDisplaceShortOriginal(t *testing.T) {
	// A long namespace declaration whose capped tail equals a later flat
	// declaration's original name must take the suffix itself: the flat tool's
	// original name is what replayed calls and tool_choice carry, so
	// displacing it would dispatch those calls to the wrong tool.
	longNamespace := "mcp__a__" + strings.Repeat("b", 60)
	longChild := "child_tool"
	qualified := longNamespace + "__" + longChild
	flatName := capResponsesChatToolName(qualified)
	if len(qualified) <= 64 || len(flatName) != 64 || flatName == qualified {
		t.Fatalf("fixture drift: qualified %q (len %d) must exceed the cap and cap to 64 chars", qualified, len(qualified))
	}
	suffixed := capResponsesChatToolName(flatName + "_1")

	toolsJSON := `[
		{
			"type":"namespace",
			"name":"` + longNamespace + `",
			"tools":[{"type":"function","name":"` + longChild + `","parameters":{"type":"object"}}]
		},
		{"type":"function","name":"` + flatName + `","parameters":{"type":"object"}}
	]`

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`
	}`), false)
	emitted := gjson.GetBytes(out, "tools").Array()
	if len(emitted) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(emitted), out)
	}
	if got := emitted[1].Get("function.name").String(); got != flatName {
		t.Fatalf("flat declaration was displaced: its name is %q, want its original %q; output=%s", got, flatName, out)
	}
	if got := emitted[0].Get("function.name").String(); got != suffixed {
		t.Fatalf("long declaration name = %q, want suffixed %q; output=%s", got, suffixed, out)
	}
	for _, tool := range emitted {
		if name := tool.Get("function.name").String(); len(name) > 64 {
			t.Errorf("function.name %q (len %d) exceeds 64; output=%s", name, len(name), out)
		}
	}

	// Replayed calls and tool_choice for the flat tool carry its original
	// name; they must resolve to the flat declaration, not to the long
	// declaration that caps onto it.
	replay := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"` + flatName + `","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools": ` + toolsJSON + `
	}`)
	replayOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", replay, false)
	for _, m := range gjson.GetBytes(replayOut, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			if got := m.Get("tool_calls.0.function.name").String(); got != flatName {
				t.Fatalf("replayed flat call resolved to %q, want %q; output=%s", got, flatName, replayOut)
			}
		}
	}

	forcedOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`,
		"tool_choice": {"type":"function","function":{"name":"`+flatName+`"}}
	}`), false)
	if got := gjson.GetBytes(forcedOut, "tool_choice.function.name").String(); got != flatName {
		t.Fatalf("tool_choice for the flat tool resolved to %q, want %q; output=%s", got, flatName, forcedOut)
	}

	// A replayed call carrying the long declaration's fully-qualified
	// uncapped name (history from an older build or a foreign client that
	// flattened the name itself) must resolve to the suffixed chat name,
	// not to the capped tail that now belongs to the flat tool.
	longReplay := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_2","name":"` + qualified + `","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_2","output":"ok"}
		],
		"tools": ` + toolsJSON + `
	}`)
	longReplayOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", longReplay, false)
	for _, m := range gjson.GetBytes(longReplayOut, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			if got := m.Get("tool_calls.0.function.name").String(); got != suffixed {
				t.Fatalf("replayed long-qualified call resolved to %q, want %q; output=%s", got, suffixed, longReplayOut)
			}
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_AmbiguousLongLocalNameStaysUnresolved(t *testing.T) {
	// Two namespace declarations sharing the same >64-byte local name, with a
	// replayed call that omits the namespace: resolution is ambiguous, and the
	// capped fallback must not land on either declaration's alias, or the call
	// would silently invoke that namespace's tool.
	longLocal := "shared_" + strings.Repeat("x", 60)
	toolsJSON := `[
		{
			"type":"namespace",
			"name":"mcp__alpha",
			"tools":[{"type":"function","name":"` + longLocal + `","parameters":{"type":"object"}}]
		},
		{
			"type":"namespace",
			"name":"mcp__beta",
			"tools":[{"type":"function","name":"` + longLocal + `","parameters":{"type":"object"}}]
		}
	]`

	replay := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"` + longLocal + `","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools": ` + toolsJSON + `
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", replay, false)

	declaredAliases := map[string]bool{}
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		declaredAliases[tool.Get("function.name").String()] = true
	}
	if len(declaredAliases) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(declaredAliases), out)
	}
	replayedName := ""
	for _, m := range gjson.GetBytes(out, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			replayedName = m.Get("tool_calls.0.function.name").String()
		}
	}
	if len(replayedName) > 64 {
		t.Fatalf("replayed ambiguous name %q (len %d) exceeds 64; output=%s", replayedName, len(replayedName), out)
	}
	if declaredAliases[replayedName] {
		t.Fatalf("ambiguous replayed call resolved to declared alias %q, silently invoking one namespace's tool; output=%s", replayedName, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_LongAliasDoesNotDisplaceNamespacedLocalName(t *testing.T) {
	// A long declaration's capped alias must not occupy the (<=64-char) local
	// name of a namespaced declaration whose qualified identity exceeds the
	// cap: replayed calls and tool_choice that omit the namespace carry the
	// bare local name, and local-name recovery must resolve them to the
	// namespaced tool instead of to the earlier long declaration's alias.
	localName := "l" + strings.Repeat("m", 63) // 64 chars
	longFlatName := strings.Repeat("n", 11) + localName
	if len(longFlatName) <= 64 || capResponsesChatToolName(longFlatName) != localName {
		t.Fatalf("fixture drift: cap(%q) = %q, want %q", longFlatName, capResponsesChatToolName(longFlatName), localName)
	}
	// mcp__beta__localName is 75 chars, so the namespaced declaration is long
	// too, and its 11-char prefix is exactly what the cap drops: its alias is
	// localName itself unless the reservation keeps the flat tool off it.
	toolsJSON := `[
		{"type":"function","name":"` + longFlatName + `","parameters":{"type":"object"}},
		{
			"type":"namespace",
			"name":"mcp__beta",
			"tools":[{"type":"function","name":"` + localName + `","parameters":{"type":"object"}}]
		}
	]`

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`
	}`), false)
	emitted := gjson.GetBytes(out, "tools").Array()
	if len(emitted) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(emitted), out)
	}
	if got := emitted[0].Get("function.name").String(); got == localName {
		t.Fatalf("long declaration claimed the namespaced local name %q as its capped alias; output=%s", localName, out)
	}
	namespacedAlias := emitted[1].Get("function.name").String()
	for i, tool := range emitted {
		if name := tool.Get("function.name").String(); len(name) > 64 {
			t.Errorf("tools[%d].function.name %q (len %d) exceeds 64; output=%s", i, name, len(name), out)
		}
	}

	// A replayed call omitting the namespace carries the bare local name and
	// must resolve to the namespaced declaration's alias, not the long one.
	replay := []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"` + localName + `","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools": ` + toolsJSON + `
	}`)
	replayOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", replay, false)
	for _, m := range gjson.GetBytes(replayOut, "messages").Array() {
		if m.Get("role").String() == "assistant" {
			if got := m.Get("tool_calls.0.function.name").String(); got != namespacedAlias {
				t.Fatalf("replayed bare local name resolved to %q, want the namespaced alias %q; output=%s", got, namespacedAlias, replayOut)
			}
		}
	}

	// tool_choice carrying the bare local name must resolve the same way.
	forcedOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`,
		"tool_choice": {"type":"function","function":{"name":"`+localName+`"}}
	}`), false)
	if got := gjson.GetBytes(forcedOut, "tool_choice.function.name").String(); got != namespacedAlias {
		t.Fatalf("tool_choice bare local name resolved to %q, want the namespaced alias %q; output=%s", got, namespacedAlias, forcedOut)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_SharedLocalNameIsNeverEmitted(t *testing.T) {
	// Two namespaces declaring the same 64-byte local name: both qualified
	// identities exceed the cap and their tails are exactly that bare local
	// name, so the name is ambiguous for any namespace-less call yet also the
	// natural capped alias of both declarations. Reserving it for the first
	// declaration alone would make it emit the ambiguous name verbatim, where
	// the exact-emitted-alias match attributes every namespace-less call and
	// tool_choice to that namespace. The name must be burned instead, leaving
	// both declarations on distinct aliases.
	sharedLocal := "s" + strings.Repeat("t", 63) // exactly 64 chars
	for _, namespace := range []string{"mcp__alpha", "mcp__beta"} {
		if got := capResponsesChatToolName(rawResponsesNamespaceQualifiedName(namespace, sharedLocal)); got != sharedLocal {
			t.Fatalf("fixture drift: %s alias = %q, want the ambiguous bare name %q", namespace, got, sharedLocal)
		}
	}
	toolsJSON := `[
		{
			"type":"namespace",
			"name":"mcp__alpha",
			"tools":[{"type":"function","name":"` + sharedLocal + `","parameters":{"type":"object"}}]
		},
		{
			"type":"namespace",
			"name":"mcp__beta",
			"tools":[{"type":"function","name":"` + sharedLocal + `","parameters":{"type":"object"}}]
		}
	]`

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`
	}`), false)
	emitted := gjson.GetBytes(out, "tools").Array()
	if len(emitted) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(emitted), out)
	}
	aliases := make(map[string]bool, len(emitted))
	for i, tool := range emitted {
		name := tool.Get("function.name").String()
		if len(name) > 64 {
			t.Errorf("tools[%d].function.name %q (len %d) exceeds 64; output=%s", i, name, len(name), out)
		}
		if name == sharedLocal {
			t.Errorf("tools[%d] emits the ambiguous local name %q; output=%s", i, name, out)
		}
		if aliases[name] {
			t.Errorf("tools[%d] duplicates alias %q; output=%s", i, name, out)
		}
		aliases[name] = true
	}
	alphaAlias := emitted[0].Get("function.name").String()
	betaAlias := emitted[1].Get("function.name").String()

	// Each namespace still reaches its own declaration.
	for _, tc := range []struct {
		namespace string
		want      string
	}{
		{"mcp__alpha", alphaAlias},
		{"mcp__beta", betaAlias},
	} {
		namespacedOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
			"input": [
				{"type":"function_call","call_id":"call_1","namespace":"`+tc.namespace+`","name":"`+sharedLocal+`","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"ok"}
			],
			"tools": `+toolsJSON+`
		}`), false)
		got := ""
		for _, m := range gjson.GetBytes(namespacedOut, "messages").Array() {
			if m.Get("role").String() == "assistant" {
				got = m.Get("tool_calls.0.function.name").String()
			}
		}
		if got != tc.want {
			t.Fatalf("namespaced replay for %s resolved to %q, want %q; output=%s", tc.namespace, got, tc.want, namespacedOut)
		}
	}

	// A namespace-less replayed call or tool_choice carrying the ambiguous
	// bare name must stay unresolved rather than pick a winner.
	bareOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"`+sharedLocal+`","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools": `+toolsJSON+`
	}`), false)
	for _, m := range gjson.GetBytes(bareOut, "messages").Array() {
		if m.Get("role").String() != "assistant" {
			continue
		}
		got := m.Get("tool_calls.0.function.name").String()
		if len(got) > 64 {
			t.Fatalf("ambiguous replayed name %q (len %d) exceeds 64; output=%s", got, len(got), bareOut)
		}
		if aliases[got] {
			t.Fatalf("ambiguous replayed call resolved to declared alias %q, silently invoking one namespace's tool; output=%s", got, bareOut)
		}
	}
	bareForced := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`,
		"tool_choice": {"type":"function","function":{"name":"`+sharedLocal+`"}}
	}`), false)
	if got := gjson.GetBytes(bareForced, "tool_choice.function.name").String(); aliases[got] {
		t.Fatalf("ambiguous tool_choice resolved to declared alias %q, silently invoking one namespace's tool; output=%s", got, bareForced)
	}
	forcedAlpha := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`,
		"tool_choice": {"type":"function","namespace":"mcp__beta","function":{"name":"`+sharedLocal+`"}}
	}`), false)
	if got := gjson.GetBytes(forcedAlpha, "tool_choice.function.name").String(); got != betaAlias {
		t.Fatalf("namespaced tool_choice resolved to %q, want %q; output=%s", got, betaAlias, forcedAlpha)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_QualifiedIdentityOutranksForeignLocalName(t *testing.T) {
	// A replayed call or tool_choice carrying a fully-qualified uncapped name
	// can match two things: the declaration whose qualified identity it is (A),
	// and a different namespace's child that literally uses that whole
	// qualified string as its own name (B). Local-name recovery runs on a bare
	// name and is a guess about the namespace, so it must not outrank the
	// identity match, or the call gets dispatched to B's tool.
	longChild := "read_" + strings.Repeat("f", 60)
	qualified := rawResponsesNamespaceQualifiedName("alpha_ns", longChild)
	if len(qualified) <= responsesChatToolNameLimit {
		t.Fatalf("fixture drift: qualified identity %q (len %d) must exceed the cap", qualified, len(qualified))
	}
	if capResponsesChatToolName(rawResponsesNamespaceQualifiedName("beta_ns", qualified)) != capResponsesChatToolName(qualified) {
		t.Fatalf("fixture drift: the two declarations must cap onto the same alias; got %q and %q",
			capResponsesChatToolName(rawResponsesNamespaceQualifiedName("beta_ns", qualified)),
			capResponsesChatToolName(qualified))
	}
	toolsJSON := `[
		{
			"type":"namespace",
			"name":"alpha_ns",
			"tools":[{"type":"function","name":"` + longChild + `","parameters":{"type":"object"}}]
		},
		{
			"type":"namespace",
			"name":"beta_ns",
			"tools":[{"type":"function","name":"` + qualified + `","parameters":{"type":"object"}}]
		}
	]`

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`
	}`), false)
	emitted := gjson.GetBytes(out, "tools").Array()
	if len(emitted) != 2 {
		t.Fatalf("tools count = %d, want 2; output=%s", len(emitted), out)
	}
	alphaAlias := emitted[0].Get("function.name").String()
	betaAlias := emitted[1].Get("function.name").String()
	if alphaAlias == betaAlias {
		t.Fatalf("both declarations emitted %q; output=%s", alphaAlias, out)
	}
	for i, tool := range emitted {
		if name := tool.Get("function.name").String(); len(name) > 64 {
			t.Errorf("tools[%d].function.name %q (len %d) exceeds 64; output=%s", i, name, len(name), out)
		}
	}

	// Bare qualified name: provenance points at the alpha declaration.
	bareOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [
			{"type":"function_call","call_id":"call_1","name":"`+qualified+`","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		],
		"tools": `+toolsJSON+`
	}`), false)
	for _, m := range gjson.GetBytes(bareOut, "messages").Array() {
		if m.Get("role").String() != "assistant" {
			continue
		}
		if got := m.Get("tool_calls.0.function.name").String(); got != alphaAlias {
			t.Fatalf("bare qualified name resolved to %q, want the identity owner's alias %q (beta's alias is %q); output=%s", got, alphaAlias, betaAlias, bareOut)
		}
	}
	bareForced := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
		"input": [{"role":"user","content":"hi"}],
		"tools": `+toolsJSON+`,
		"tool_choice": {"type":"function","function":{"name":"`+qualified+`"}}
	}`), false)
	if got := gjson.GetBytes(bareForced, "tool_choice.function.name").String(); got != alphaAlias {
		t.Fatalf("tool_choice bare qualified name resolved to %q, want %q; output=%s", got, alphaAlias, bareForced)
	}

	// Both namespaces stay individually reachable when the namespace is present.
	for _, tc := range []struct {
		namespace string
		name      string
		want      string
	}{
		{"alpha_ns", longChild, alphaAlias},
		{"beta_ns", qualified, betaAlias},
	} {
		namespacedOut := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("z-ai/glm-5.3-free", []byte(`{
			"input": [
				{"type":"function_call","call_id":"call_1","namespace":"`+tc.namespace+`","name":"`+tc.name+`","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"ok"}
			],
			"tools": `+toolsJSON+`
		}`), false)
		got := ""
		for _, m := range gjson.GetBytes(namespacedOut, "messages").Array() {
			if m.Get("role").String() == "assistant" {
				got = m.Get("tool_calls.0.function.name").String()
			}
		}
		if got != tc.want {
			t.Fatalf("namespaced replay for %s/%s resolved to %q, want %q; output=%s", tc.namespace, tc.name, got, tc.want, namespacedOut)
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_IncompleteToolCallsDoNotDeferMessages(t *testing.T) {
	// If assistant issues function_call(a) and function_call(b), but only function_call_output(a)
	// arrives, the history is incomplete. Intervening messages (e.g. user reminder) must NOT
	// be deferred; original order must be preserved.
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
			{"role":"user","content":"reminder before results"},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Incomplete history must stay untouched:
	// messages[0]: assistant (tool_calls [call_a, call_b])
	// messages[1]: user ("reminder before results")
	// messages[2]: tool (tool_call_id: call_a)
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" || roles[2] != "tool" {
		t.Fatalf("expected untouched order [assistant, user, tool], got: %v (output=%s)", roles, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_CompleteToolCallsDoPairMessages(t *testing.T) {
	// If assistant issues function_call(a) and function_call(b), and both function_call_output(a)
	// and function_call_output(b) arrive, with an intervening user reminder, the tool outputs
	// must be paired immediately following the assistant tool_calls message.
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"call_a","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"call_b","name":"tool_b","arguments":"{}"},
			{"role":"user","content":"reminder during execution"},
			{"type":"function_call_output","call_id":"call_b","output":"result_b"},
			{"type":"function_call_output","call_id":"call_a","output":"result_a"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Expected:
	// messages[0]: assistant (tool_calls [call_a, call_b])
	// messages[1]: tool
	// messages[2]: tool
	// messages[3]: user ("reminder during execution")
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "tool" || roles[2] != "tool" || roles[3] != "user" {
		t.Fatalf("expected order [assistant, tool, tool, user], got: %v (output=%s)", roles, out)
	}
	toolIDs := []string{messages[1].Get("tool_call_id").String(), messages[2].Get("tool_call_id").String()}
	if toolIDs[0] != "call_b" || toolIDs[1] != "call_a" {
		t.Fatalf("expected tool messages in relative input order [call_b, call_a], got: %v", toolIDs)
	}
	if messages[1].Get("content").String() != "result_b" || messages[2].Get("content").String() != "result_a" {
		t.Fatalf("expected contents [result_b, result_a], got [%s, %s]", messages[1].Get("content").String(), messages[2].Get("content").String())
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MixedEmptyIDDoesNotReorder(t *testing.T) {
	// Mixed empty call_id and valid call_a: incomplete/ambiguous history must remain untouched
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"","name":"unknown","arguments":"{}"},
			{"type":"function_call","call_id":"a","name":"known","arguments":"{}"},
			{"role":"user","content":"reminder"},
			{"type":"function_call_output","call_id":"a","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user -> tool
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" || roles[2] != "tool" {
		t.Fatalf("expected untouched order [assistant, user, tool], got: %v (output=%s)", roles, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DuplicateCallIDDoesNotReorder(t *testing.T) {
	// Duplicate call_id: ambiguous history must remain untouched
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"dup","name":"tool_1","arguments":"{}"},
			{"type":"function_call","call_id":"dup","name":"tool_2","arguments":"{}"},
			{"role":"user","content":"reminder"},
			{"type":"function_call_output","call_id":"dup","output":"ok"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user -> tool
	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" || roles[2] != "tool" {
		t.Fatalf("expected untouched order [assistant, user, tool], got: %v (output=%s)", roles, out)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DuplicateOutputCallIDDoesNotReorder(t *testing.T) {
	// Duplicate function_call_output for the same call_id: ambiguous results must not be prematurely reordered
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"call_dup_out","name":"tool_a","arguments":"{}"},
			{"role":"user","content":"reminder before results"},
			{"type":"function_call_output","call_id":"call_dup_out","output":"first"},
			{"type":"function_call_output","call_id":"call_dup_out","output":"second"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user ("reminder before results") -> tool/user
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" {
		t.Fatalf("expected natural order with user reminder preserved at index 1, got roles: %v (output=%s)", roles, out)
	}
	if messages[1].Get("content").String() != "reminder before results" {
		t.Fatalf("expected message[1] to be reminder, got: %s", messages[1].Raw)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_DuplicateCustomOutputCallIDDoesNotReorder(t *testing.T) {
	// Duplicate custom_tool_call_output for the same call_id: ambiguous results must not be prematurely reordered
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"custom_tool_call","call_id":"custom_dup","name":"custom_a","input":"{}"},
			{"role":"user","content":"reminder before custom results"},
			{"type":"custom_tool_call_output","call_id":"custom_dup","output":"output 1"},
			{"type":"custom_tool_call_output","call_id":"custom_dup","output":"output 2"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user ("reminder before custom results") -> tool/user
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" {
		t.Fatalf("expected natural order with user reminder preserved at index 1, got roles: %v (output=%s)", roles, out)
	}
	if messages[1].Get("content").String() != "reminder before custom results" {
		t.Fatalf("expected message[1] to be reminder, got: %s", messages[1].Raw)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MultipleOutputsWithoutIDDoNotGuessOrReorder(t *testing.T) {
	// If assistant issues function_call(a) and function_call(b), and multiple outputs arrive
	// without call_ids, the assignment is a non-unique guess. The history must stay untouched!
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"a","name":"unknown_a","arguments":"{}"},
			{"type":"function_call","call_id":"b","name":"unknown_b","arguments":"{}"},
			{"role":"user","content":"reminder before results"},
			{"type":"function_call_output","output":"output X"},
			{"type":"function_call_output","output":"output Y"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user ("reminder before results") -> user ("output X") -> user ("output Y")
	// Crucially, outputs X and Y must NOT be given guessed tool_call_id "a" or "b" and must NOT be fabricated as tool messages.
	if len(messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" || roles[2] != "user" || roles[3] != "user" {
		t.Fatalf("expected natural order with user reminder and un-guessed outputs preserved, got roles: %v (output=%s)", roles, out)
	}
	if messages[1].Get("content").String() != "reminder before results" {
		t.Fatalf("expected message[1] to be reminder, got: %s", messages[1].Raw)
	}
	if messages[2].Get("content").String() != "output X" || messages[3].Get("content").String() != "output Y" {
		t.Fatalf("expected outputs X and Y as standalone user messages, got msg2=%s msg3=%s", messages[2].Raw, messages[3].Raw)
	}
	// Verify no tool messages were created with guessed call IDs
	for _, m := range messages {
		if m.Get("role").String() == "tool" {
			t.Fatalf("unexpected tool message created via guessing: %s", m.Raw)
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MultipleOutputsWithoutIDAndOrphanOutputDoNotGuessOrReorder(t *testing.T) {
	// Call A, Call B.
	// User reminder.
	// Output 1 has NO ID (X).
	// Output 2 has NO ID (Y).
	// Output 3 has orphan explicit ID (Z).
	// The presence of orphan_id must NOT cause X/Y to be guessed and reordered before the reminder!
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"type":"function_call","call_id":"a","name":"tool_a","arguments":"{}"},
			{"type":"function_call","call_id":"b","name":"tool_b","arguments":"{}"},
			{"role":"user","content":"reminder before results"},
			{"type":"function_call_output","output":"output X"},
			{"type":"function_call_output","output":"output Y"},
			{"type":"function_call_output","call_id":"orphan_id","output":"output Z"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	roles := make([]string, 0, len(messages))
	for _, m := range messages {
		roles = append(roles, m.Get("role").String())
	}

	// Must stay in natural input order: assistant -> user ("reminder before results") -> user (X) -> user (Y) -> user (Z)
	// Outputs X and Y must NOT be guessed as tool calls "a" and "b" and must NOT be moved before reminder!
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d: %s", len(messages), out)
	}
	if roles[0] != "assistant" || roles[1] != "user" {
		t.Fatalf("expected natural order with user reminder preserved at index 1 without guessing, got roles: %v (output=%s)", roles, out)
	}
	if messages[1].Get("content").String() != "reminder before results" {
		t.Fatalf("expected message[1] to be reminder, got: %s", messages[1].Raw)
	}
	// Verify no tool messages were created with guessed call IDs
	for _, m := range messages {
		if m.Get("role").String() == "tool" {
			t.Fatalf("unexpected tool message created via guessing: %s", m.Raw)
		}
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_MapsMaxOutputTokensToMaxTokens(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.4",
		"input": "hello",
		"max_output_tokens": 1024
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", raw, false)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 1024 {
		t.Fatalf("max_tokens = %d, want 1024; output=%s", got, string(out))
	}
	if gjson.GetBytes(out, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent; output=%s", string(out))
	}

	rawWithoutLimit := []byte(`{
		"model": "gpt-5.4",
		"input": "hello"
	}`)

	outWithoutLimit := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawWithoutLimit, false)
	if gjson.GetBytes(outWithoutLimit, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent when omitted; output=%s", string(outWithoutLimit))
	}
	if gjson.GetBytes(outWithoutLimit, "max_tokens").Exists() {
		t.Fatalf("max_tokens should be absent when omitted; output=%s", string(outWithoutLimit))
	}

	rawNull := []byte(`{
		"model": "gpt-5.4",
		"input": "hello",
		"max_output_tokens": null
	}`)

	outNull := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", rawNull, false)
	if got := gjson.GetBytes(outNull, "max_tokens"); !got.Exists() || got.Type != gjson.Null {
		t.Fatalf("max_tokens = %v, want null; output=%s", got, string(outNull))
	}
	if gjson.GetBytes(outNull, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens should be absent; output=%s", string(outNull))
	}
}
