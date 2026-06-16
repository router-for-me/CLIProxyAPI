package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

// customToolRequest is a Responses-API request that declares apply_patch as a
// freeform `custom` tool plus a regular `function` tool (shell). The response
// side uses the request to decide which returned tool calls must be re-emitted
// as custom_tool_call (bare input) vs function_call (JSON arguments).
const customToolRequest = `{
  "model": "gpt-5",
  "tools": [
    {"type": "custom", "name": "apply_patch", "description": "Use the apply_patch tool to edit files."},
    {"type": "function", "name": "shell", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}}
  ]
}`

const barePatch = "*** Begin Patch\n*** Add File: /tmp/x.txt\n+hi\n*** End Patch"

// geminiArgsWithInput wraps the bare patch text the way Gemini emits it after
// the request-side downgrade: the function is declared with a single string
// parameter named "input", so the model returns args={"input":"<patch>"}.
func geminiArgsWithInput(t *testing.T, input string) string {
	t.Helper()
	return `{"input":` + jsonQuote(input) + `}`
}

func jsonQuote(s string) string {
	// Minimal JSON string quoting sufficient for the test payloads.
	out := []rune{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\t':
			out = append(out, '\\', 't')
		case '\r':
			out = append(out, '\\', 'r')
		default:
			out = append(out, r)
		}
	}
	out = append(out, '"')
	return string(out)
}

// TestConvertGeminiResponseStream_ApplyPatchCustomToolCall verifies that an
// apply_patch (declared as a custom tool) functionCall coming back from Gemini
// is re-emitted as a custom_tool_call with bare `input` text and no arguments.
func TestConvertGeminiResponseStream_ApplyPatchCustomToolCall(t *testing.T) {
	args := geminiArgsWithInput(t, barePatch)
	lines := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"apply_patch","args":` + args + `}}]}}],"responseId":"req_ct_1"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"responseId":"req_ct_1"}`,
	}

	var param any
	var out [][]byte
	for _, line := range lines {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", []byte(customToolRequest), nil, []byte(line), &param)...)
	}

	var (
		gotAdded     bool
		gotInputDone bool
		gotItemDone  bool
		gotCompleted bool
	)

	for _, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.output_item.added":
			if data.Get("item.type").String() == "custom_tool_call" {
				gotAdded = true
				if data.Get("item.name").String() != "apply_patch" {
					t.Fatalf("added item name = %q, want apply_patch", data.Get("item.name").String())
				}
				if data.Get("item.arguments").Exists() {
					t.Fatalf("custom_tool_call added must not carry arguments: %s", data.Raw)
				}
			}
		case "response.custom_tool_call_input.done":
			gotInputDone = true
			if data.Get("input").String() != barePatch {
				t.Fatalf("input.done input = %q, want bare patch %q", data.Get("input").String(), barePatch)
			}
		case "response.output_item.done":
			if data.Get("item.type").String() == "custom_tool_call" {
				gotItemDone = true
				if data.Get("item.input").String() != barePatch {
					t.Fatalf("item.done input = %q, want bare patch", data.Get("item.input").String())
				}
				if data.Get("item.arguments").Exists() {
					t.Fatalf("custom_tool_call done must not carry arguments: %s", data.Raw)
				}
			}
		case "response.completed":
			gotCompleted = true
			outputs := data.Get("response.output")
			found := false
			outputs.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "custom_tool_call" {
					found = true
					if item.Get("input").String() != barePatch {
						t.Fatalf("completed custom_tool_call input = %q, want bare patch", item.Get("input").String())
					}
					if item.Get("arguments").Exists() {
						t.Fatalf("completed custom_tool_call must not carry arguments: %s", item.Raw)
					}
				}
				return true
			})
			if !found {
				t.Fatalf("response.completed output missing custom_tool_call: %s", data.Raw)
			}
		}
	}

	if !gotAdded || !gotInputDone || !gotItemDone || !gotCompleted {
		t.Fatalf("missing events: added=%v inputDone=%v itemDone=%v completed=%v", gotAdded, gotInputDone, gotItemDone, gotCompleted)
	}
}

// TestConvertGeminiResponseStream_RegularFunctionStillFunctionCall verifies a
// normal function tool (shell) still produces a function_call with JSON args.
func TestConvertGeminiResponseStream_RegularFunctionStillFunctionCall(t *testing.T) {
	lines := []string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"shell","args":{"cmd":"ls"}}}]}}],"responseId":"req_fn_1"}`,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"responseId":"req_fn_1"}`,
	}

	var param any
	var out [][]byte
	for _, line := range lines {
		out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "test-model", []byte(customToolRequest), nil, []byte(line), &param)...)
	}

	gotFuncAdded := false
	for _, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		if ev == "response.output_item.added" && data.Get("item.type").String() == "function_call" {
			gotFuncAdded = true
			if data.Get("item.name").String() != "shell" {
				t.Fatalf("function_call name = %q, want shell", data.Get("item.name").String())
			}
		}
		if ev == "response.output_item.added" && data.Get("item.type").String() == "custom_tool_call" {
			t.Fatalf("regular function must not become custom_tool_call: %s", data.Raw)
		}
		if ev == "response.completed" {
			data.Get("response.output").ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "function_call" {
					if item.Get("arguments").String() != `{"cmd":"ls"}` {
						t.Fatalf("function_call arguments = %q", item.Get("arguments").String())
					}
				}
				return true
			})
		}
	}
	if !gotFuncAdded {
		t.Fatalf("expected a function_call output_item.added for shell")
	}
}

// TestConvertGeminiResponseNonStream_ApplyPatchCustomToolCall verifies the
// non-streaming aggregator emits a custom_tool_call with bare input.
func TestConvertGeminiResponseNonStream_ApplyPatchCustomToolCall(t *testing.T) {
	args := geminiArgsWithInput(t, barePatch)
	raw := `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"apply_patch","args":` + args + `}}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"modelVersion":"test-model","responseId":"req_ct_ns_1"}`

	var param any
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "test-model", []byte(customToolRequest), nil, []byte(raw), &param)

	root := gjson.ParseBytes(out)
	found := false
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "custom_tool_call" {
			found = true
			if item.Get("name").String() != "apply_patch" {
				t.Fatalf("custom_tool_call name = %q, want apply_patch", item.Get("name").String())
			}
			if item.Get("input").String() != barePatch {
				t.Fatalf("custom_tool_call input = %q, want bare patch", item.Get("input").String())
			}
			if item.Get("arguments").Exists() {
				t.Fatalf("custom_tool_call must not carry arguments: %s", item.Raw)
			}
		}
		return true
	})
	if !found {
		t.Fatalf("non-stream output missing custom_tool_call: %s", string(out))
	}
}

// TestConvertGeminiResponseNonStream_RegularFunctionStillFunctionCall verifies a
// normal function still produces function_call with JSON arguments (non-stream).
func TestConvertGeminiResponseNonStream_RegularFunctionStillFunctionCall(t *testing.T) {
	raw := `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"shell","args":{"cmd":"ls"}}}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"modelVersion":"test-model","responseId":"req_fn_ns_1"}`

	var param any
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "test-model", []byte(customToolRequest), nil, []byte(raw), &param)

	root := gjson.ParseBytes(out)
	found := false
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "function_call" && item.Get("name").String() == "shell" {
			found = true
			if item.Get("arguments").String() != `{"cmd":"ls"}` {
				t.Fatalf("function_call arguments = %q", item.Get("arguments").String())
			}
		}
		if item.Get("type").String() == "custom_tool_call" {
			t.Fatalf("regular function must not become custom_tool_call: %s", item.Raw)
		}
		return true
	})
	if !found {
		t.Fatalf("non-stream output missing shell function_call: %s", string(out))
	}
}
