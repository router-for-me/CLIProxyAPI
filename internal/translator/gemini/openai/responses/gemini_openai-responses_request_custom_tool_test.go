package responses

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Regression tests for #7422: custom_tool_call / custom_tool_call_output input
// items must translate into exactly the same Gemini payload as the equivalent
// function_call / function_call_output items, so freeform custom tool replay
// can never emit a divergent shape that stalls the backend with zero bytes.

const customToolItemsPayload = `{
	"model": "gemini-4-argon-high",
	"tools": [
		{"type": "custom", "name": "exec", "description": "run js"},
		{"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}
	],
	"input": [
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
		{"type": "reasoning", "id": "rs_1", "summary": []},
		{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": "console.log('a')"},
		{"type": "custom_tool_call_output", "call_id": "c1", "output": "a` + "\n" + `"},
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go on"}]},
		{"type": "custom_tool_call", "call_id": "c2", "name": "exec", "input": "console.log('b')"},
		{"type": "custom_tool_call_output", "call_id": "c2", "output": [{"type": "output_text", "text": "b1"}, {"type": "output_text", "text": "b2"}, {"type": "input_image", "image_url": "https://example.com/b.png"}]}
	]
}`

// functionToolItemsPayload is the reporter-validated rewrite of
// customToolItemsPayload: identical content expressed as function_call items.
const functionToolItemsPayload = `{
	"model": "gemini-4-argon-high",
	"tools": [
		{"type": "custom", "name": "exec", "description": "run js"},
		{"type": "function", "name": "read_file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}
	],
	"input": [
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
		{"type": "reasoning", "id": "rs_1", "summary": []},
		{"type": "function_call", "call_id": "c1", "name": "exec", "arguments": "{\"input\":\"console.log('a')\"}"},
		{"type": "function_call_output", "call_id": "c1", "output": "a` + "\n" + `"},
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "go on"}]},
		{"type": "function_call", "call_id": "c2", "name": "exec", "arguments": "{\"input\":\"console.log('b')\"}"},
		{"type": "function_call_output", "call_id": "c2", "output": "b1` + "\n" + `b2` + "\n" + `{\"type\": \"input_image\", \"image_url\": \"https://example.com/b.png\"}"}
	]
}`

func TestConvertOpenAIResponsesRequestToGeminiCustomToolItemsMatchFunctionItems(t *testing.T) {
	customOut, errCustom := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(customToolItemsPayload), true)
	if errCustom != nil {
		t.Fatalf("custom tool payload conversion failed: %v", errCustom)
	}
	functionOut, errFunction := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(functionToolItemsPayload), true)
	if errFunction != nil {
		t.Fatalf("function tool payload conversion failed: %v", errFunction)
	}
	if string(customOut) != string(functionOut) {
		t.Fatalf("custom tool items must translate identically to the validated function_call rewrite\ncustom:   %s\nfunction: %s", customOut, functionOut)
	}
}

func TestConvertOpenAIResponsesRequestToGeminiCustomToolItemsEmitValidFunctionShapes(t *testing.T) {
	output, errConvert := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(customToolItemsPayload), true)
	if errConvert != nil {
		t.Fatalf("conversion failed: %v", errConvert)
	}
	root := ojson.GetBytesNoCopy(output, "contents")
	if !root.IsArray() {
		t.Fatalf("missing contents array: %s", output)
	}

	var callArgs []ojson.Result
	var responseResults []ojson.Result
	root.ForEach(func(_, content ojson.Result) bool {
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
			if fc := part.Get("functionCall"); fc.Exists() {
				callArgs = append(callArgs, fc.Get("args"))
			}
			if fr := part.Get("functionResponse"); fr.Exists() {
				responseResults = append(responseResults, fr.Get("response"))
			}
			return true
		})
		return true
	})

	if len(callArgs) != 2 {
		t.Fatalf("expected 2 functionCall parts, got %d; raw: %s", len(callArgs), output)
	}
	for idx, want := range []string{"console.log('a')", "console.log('b')"} {
		if got := callArgs[idx].Get("input").String(); got != want {
			t.Fatalf("functionCall %d args.input = %q, want %q", idx, got, want)
		}
	}

	if len(responseResults) != 2 {
		t.Fatalf("expected 2 functionResponse parts, got %d; raw: %s", len(responseResults), output)
	}
	// Structured custom tool outputs must land as plain strings inside
	// functionResponse.response.result, never as raw arrays or objects.
	for idx, result := range responseResults {
		if result.Get("result").Type != ojson.String {
			t.Fatalf("functionResponse %d response.result must be a string, got %s", idx, result.Raw)
		}
	}
	if got := responseResults[1].Get("result").String(); got != "b1\nb2\n{\"type\": \"input_image\", \"image_url\": \"https://example.com/b.png\"}" {
		t.Fatalf("joined custom tool output = %q, want %q", got, "b1\nb2\n{\"type\": \"input_image\", \"image_url\": \"https://example.com/b.png\"}")
	}
}

func TestConvertOpenAIResponsesRequestToGeminiFunctionCallItemsUnchanged(t *testing.T) {
	// Already-correct function_call items must keep translating exactly as
	// before: parsed object arguments and stringified outputs.
	output, errConvert := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(functionToolItemsPayload), false)
	if errConvert != nil {
		t.Fatalf("conversion failed: %v", errConvert)
	}
	contents := ojson.GetBytesNoCopy(output, "contents").Array()

	callPart := contents[1].Get("parts.0.functionCall")
	if !callPart.Exists() {
		t.Fatalf("missing functionCall: %s", output)
	}
	if got := callPart.Get("name").String(); got != "exec" {
		t.Fatalf("functionCall name = %q, want exec", got)
	}
	if got := callPart.Get("id").String(); got != "c1" {
		t.Fatalf("functionCall id = %q, want c1", got)
	}
	if got := callPart.Get("args.input").String(); got != "console.log('a')" {
		t.Fatalf("functionCall args.input = %q, want console.log('a')", got)
	}

	respPart := contents[2].Get("parts.0.functionResponse")
	if !respPart.Exists() {
		t.Fatalf("missing functionResponse: %s", output)
	}
	if got := respPart.Get("response.result").String(); got != "a\n" {
		t.Fatalf("functionResponse result = %q, want %q", got, "a\n")
	}
}

func TestConvertOpenAIResponsesRequestToGeminiCustomToolItemsWithoutRequiredFields(t *testing.T) {
	// Degenerate custom tool items must convert promptly into a valid request
	// instead of hanging or emitting malformed contents.
	inputJSON := `{
		"model": "gemini-4-argon-high",
		"tools": [{"type": "custom", "name": "exec"}],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "custom_tool_call"},
			{"type": "custom_tool_call", "call_id": "c9"},
			{"type": "custom_tool_call", "call_id": "c10", "name": "exec"},
			{"type": "custom_tool_call_output", "call_id": "c10"},
			{"type": "custom_tool_call_output"},
			{"type": "custom_tool_call", "call_id": "c11", "name": "exec", "input": {"nested": true}},
			{"type": "custom_tool_call_output", "call_id": "c11", "output": {"ok": true}}
		]
	}`

	output, errConvert := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(inputJSON), false)
	if errConvert != nil {
		t.Fatalf("conversion failed: %v", errConvert)
	}
	if !ojson.Valid(string(output)) {
		t.Fatalf("output is not valid JSON: %s", output)
	}
	if strings.Contains(string(output), "custom_tool_call") {
		t.Fatalf("custom tool item types leaked into the Gemini payload: %s", output)
	}

	// The object-input call keeps its structured args; the object output stays
	// an object, matching the equivalent function_call_output behavior.
	if got := ojson.GetBytesNoCopy(output, "contents.3.parts.0.functionCall.args.input.nested").Raw; got != "true" {
		t.Fatalf("structured custom tool input should survive as args.input.nested, got %q; raw: %s", got, output)
	}
	if got := ojson.GetBytesNoCopy(output, "contents.4.parts.0.functionResponse.response.result.ok").Raw; got != "true" {
		t.Fatalf("structured custom tool output should survive as result.ok, got %q; raw: %s", got, output)
	}
}

func TestConvertOpenAIResponsesRequestToGeminiCustomToolItemsPreserveNamespace(t *testing.T) {
	inputJSON := `{
		"model": "gemini-4-argon-high",
		"tools": [{"type": "namespace", "name": "functions", "tools": [{"type": "custom", "name": "exec"}]}],
		"input": [
			{"type": "custom_tool_call", "call_id": "c1", "namespace": "functions", "name": "exec", "input": "pwd"},
			{"type": "custom_tool_call_output", "call_id": "c1", "output": "/workspace"}
		]
	}`

	output, errConvert := ConvertOpenAIResponsesRequestToGemini("gemini-4-argon-high", []byte(inputJSON), false)
	if errConvert != nil {
		t.Fatalf("conversion failed: %v", errConvert)
	}
	contents := ojson.GetBytesNoCopy(output, "contents").Array()
	if len(contents) < 2 {
		t.Fatalf("expected at least 2 contents, got %d; raw: %s", len(contents), output)
	}
	if got := contents[0].Get("parts.0.functionCall.name").String(); got != "functions__exec" {
		t.Fatalf("functionCall name = %q, want functions__exec", got)
	}
	if got := contents[0].Get("parts.0.functionCall.args.input").String(); got != "pwd" {
		t.Fatalf("functionCall args.input = %q, want pwd", got)
	}
	if got := contents[1].Get("parts.0.functionResponse.response.result").String(); got != "/workspace" {
		t.Fatalf("functionResponse result = %q, want /workspace", got)
	}
}
