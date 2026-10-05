package responses

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var benchmarkConvertSystemRoleOutput []byte
var benchmarkConvertNormalizedOutput []byte

// TestConvertSystemRoleToDeveloper_BasicConversion tests the basic system -> developer role conversion
func TestConvertSystemRoleToDeveloper_BasicConversion(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are a pirate."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Say hello."}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that system role was converted to developer
	firstItemRole := gjson.Get(outputStr, "input.0.role")
	if firstItemRole.String() != "developer" {
		t.Errorf("Expected role 'developer', got '%s'", firstItemRole.String())
	}

	// Check that user role remains unchanged
	secondItemRole := gjson.Get(outputStr, "input.1.role")
	if secondItemRole.String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", secondItemRole.String())
	}

	// Check content is preserved
	firstItemContent := gjson.Get(outputStr, "input.0.content.0.text")
	if firstItemContent.String() != "You are a pirate." {
		t.Errorf("Expected content 'You are a pirate.', got '%s'", firstItemContent.String())
	}
}

// TestConvertSystemRoleToDeveloper_MultipleSystemMessages tests conversion with multiple system messages
func TestConvertSystemRoleToDeveloper_MultipleSystemMessages(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are helpful."}]
			},
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "Be concise."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that both system roles were converted
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected first role 'developer', got '%s'", firstRole.String())
	}

	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "developer" {
		t.Errorf("Expected second role 'developer', got '%s'", secondRole.String())
	}

	// Check that user role is unchanged
	thirdRole := gjson.Get(outputStr, "input.2.role")
	if thirdRole.String() != "user" {
		t.Errorf("Expected third role 'user', got '%s'", thirdRole.String())
	}
}

// TestConvertSystemRoleToDeveloper_NoSystemMessages tests that requests without system messages are unchanged
func TestConvertSystemRoleToDeveloper_NoSystemMessages(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hi there!"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that user and assistant roles are unchanged
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", firstRole.String())
	}

	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "assistant" {
		t.Errorf("Expected role 'assistant', got '%s'", secondRole.String())
	}
}

// TestConvertSystemRoleToDeveloper_EmptyInput tests that empty input arrays are handled correctly
func TestConvertSystemRoleToDeveloper_EmptyInput(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": []
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that input is still an empty array
	inputArray := gjson.Get(outputStr, "input")
	if !inputArray.IsArray() {
		t.Error("Input should still be an array")
	}
	if len(inputArray.Array()) != 0 {
		t.Errorf("Expected empty array, got %d items", len(inputArray.Array()))
	}
}

// TestConvertSystemRoleToDeveloper_NoInputField tests that requests without input field are unchanged
func TestConvertSystemRoleToDeveloper_NoInputField(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"stream": false
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check that other fields are still set correctly
	stream := gjson.Get(outputStr, "stream")
	if !stream.Bool() {
		t.Error("Stream should be set to true by conversion")
	}

	store := gjson.Get(outputStr, "store")
	if store.Bool() {
		t.Error("Store should be set to false by conversion")
	}
}

// TestConvertOpenAIResponsesRequestToCodex_OriginalIssue tests the exact issue reported by the user
func TestConvertOpenAIResponsesRequestToCodex_OriginalIssue(t *testing.T) {
	// This is the exact input that was failing with "System messages are not allowed"
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": "You are a pirate. Always respond in pirate speak."
			},
			{
				"type": "message",
				"role": "user",
				"content": "Say hello."
			}
		],
		"stream": false
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Verify system role was converted to developer
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected role 'developer', got '%s'", firstRole.String())
	}

	// Verify stream was set to true (as required by Codex)
	stream := gjson.Get(outputStr, "stream")
	if !stream.Bool() {
		t.Error("Stream should be set to true")
	}

	// Verify other required fields for Codex
	store := gjson.Get(outputStr, "store")
	if store.Bool() {
		t.Error("Store should be false")
	}

	parallelCalls := gjson.Get(outputStr, "parallel_tool_calls")
	if !parallelCalls.Bool() {
		t.Error("parallel_tool_calls should be true")
	}

	include := gjson.Get(outputStr, "include")
	if !include.IsArray() || len(include.Array()) != 1 {
		t.Error("include should be an array with one element")
	} else if include.Array()[0].String() != "reasoning.encrypted_content" {
		t.Errorf("Expected include[0] to be 'reasoning.encrypted_content', got '%s'", include.Array()[0].String())
	}
}

func TestConvertOpenAIResponsesRequestToCodexReusesNormalizedPayload(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.6","stream":true,"store":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"service_tier":"priority","input":[{"type":"message","role":"user","content":"hello"}]}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)

	if &output[0] != &inputJSON[0] {
		t.Fatal("normalized request payload was copied")
	}
	if string(output) != string(inputJSON) {
		t.Fatalf("normalized request changed:\n got: %s\nwant: %s", output, inputJSON)
	}
}

func TestConvertOpenAIResponsesRequestToCodexNormalizesRequiredFields(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gpt-5.6",
		"stream":"true",
		"store":true,
		"parallel_tool_calls":false,
		"include":["file_search_call.results","reasoning.encrypted_content"],
		"max_output_tokens":4096,
		"max_completion_tokens":4096,
		"temperature":0.2,
		"top_p":0.9,
		"service_tier":"standard",
		"truncation":"auto",
		"prompt_cache_options":{"mode":"implicit"},
		"prompt_cache_retention":"24h",
		"user":"request-owner",
		"input":[{"type":"message","role":"system","content":"hello"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)

	if stream := gjson.GetBytes(output, "stream"); stream.Type != gjson.True {
		t.Fatalf("stream = %s, want true", stream.Raw)
	}
	if store := gjson.GetBytes(output, "store"); store.Type != gjson.False {
		t.Fatalf("store = %s, want false", store.Raw)
	}
	if parallel := gjson.GetBytes(output, "parallel_tool_calls"); parallel.Type != gjson.True {
		t.Fatalf("parallel_tool_calls = %s, want true", parallel.Raw)
	}
	include := gjson.GetBytes(output, "include").Array()
	if len(include) != 1 || include[0].Type != gjson.String || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only", gjson.GetBytes(output, "include").Raw)
	}
	if role := gjson.GetBytes(output, "input.0.role").String(); role != "developer" {
		t.Fatalf("input.0.role = %q, want developer", role)
	}
	for _, path := range []string{
		"max_output_tokens",
		"max_completion_tokens",
		"temperature",
		"top_p",
		"service_tier",
		"truncation",
		"prompt_cache_options",
		"prompt_cache_retention",
		"user",
	} {
		if gjson.GetBytes(output, path).Exists() {
			t.Fatalf("%s should be removed: %s", path, output)
		}
	}
}

func TestConvertOpenAIResponsesRequestToCodex_PreservesWebSearchSourcesInclude(t *testing.T) {
	reasoningOnly := []string{"reasoning.encrypted_content"}
	reasoningAndSources := []string{"reasoning.encrypted_content", "web_search_call.action.sources"}
	tests := []struct {
		name        string
		includeJSON string
		want        []string
	}{
		{name: "missing include", includeJSON: "", want: reasoningOnly},
		{name: "null include", includeJSON: "null", want: reasoningOnly},
		{name: "string include", includeJSON: `"web_search_call.action.sources"`, want: reasoningOnly},
		{name: "object include", includeJSON: `{"web_search_call.action.sources":true}`, want: reasoningOnly},
		{name: "non-string entries", includeJSON: `[42,true]`, want: reasoningOnly},
		{name: "empty array", includeJSON: `[]`, want: reasoningOnly},
		{name: "sources alone", includeJSON: `["web_search_call.action.sources"]`, want: reasoningAndSources},
		{name: "reasoning then sources", includeJSON: `["reasoning.encrypted_content","web_search_call.action.sources"]`, want: reasoningAndSources},
		{name: "sources before reasoning", includeJSON: `["web_search_call.action.sources","reasoning.encrypted_content"]`, want: reasoningAndSources},
		{name: "duplicate sources", includeJSON: `["web_search_call.action.sources","web_search_call.action.sources"]`, want: reasoningAndSources},
		{name: "unsupported entries filtered", includeJSON: `["file_search_call.results","web_search_call.action.sources","code_interpreter_call.outputs"]`, want: reasoningAndSources},
		{name: "non-string entries alongside sources", includeJSON: `[42,"web_search_call.action.sources",null]`, want: reasoningAndSources},
	}

	for _, stream := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("stream_%v/%s", stream, tt.name), func(t *testing.T) {
				inputJSON := []byte(`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":"hi"}]}`)
				if tt.includeJSON != "" {
					var errSet error
					inputJSON, errSet = sjson.SetRawBytes(inputJSON, "include", []byte(tt.includeJSON))
					if errSet != nil {
						t.Fatalf("setting include: %v", errSet)
					}
				}

				before := string(inputJSON)
				output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, stream)
				if string(inputJSON) != before {
					t.Fatalf("caller input changed:\n got: %s\nwant: %s", inputJSON, before)
				}
				got := gjson.GetBytes(output, "include").Array()
				if len(got) != len(tt.want) {
					t.Fatalf("include = %s, want %v", gjson.GetBytes(output, "include").Raw, tt.want)
				}
				for i, want := range tt.want {
					if got[i].Type != gjson.String || got[i].String() != want {
						t.Fatalf("include[%d] = %s, want %q; include = %s", i, got[i].Raw, want, gjson.GetBytes(output, "include").Raw)
					}
				}
			})
		}
	}
}

func TestConvertOpenAIResponsesRequestToCodex_WebSearchToolDoesNotOptIntoSources(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.6","input":"find python asyncio docs","tools":[{"type":"web_search"}],"tool_choice":"required"}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)

	include := gjson.GetBytes(output, "include").Array()
	if len(include) != 1 || include[0].Type != gjson.String || include[0].String() != "reasoning.encrypted_content" {
		t.Fatalf("include = %s, want reasoning.encrypted_content only", gjson.GetBytes(output, "include").Raw)
	}
}

func TestConvertOpenAIResponsesRequestToCodexReusesNormalizedPayloadWithSources(t *testing.T) {
	for _, includeJSON := range []string{
		`["reasoning.encrypted_content","web_search_call.action.sources"]`,
		`[ "reasoning.encrypted_content" , "web_search_call.action.sources" ]`,
		`[ "reasoning.encrypted_content" ]`,
	} {
		inputJSON := []byte(`{"model":"gpt-5.6","stream":true,"store":false,"parallel_tool_calls":true,"include":` + includeJSON + `,"service_tier":"priority","input":[{"type":"message","role":"user","content":"hello"}]}`)

		output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)

		if &output[0] != &inputJSON[0] {
			t.Fatalf("normalized request payload with include %s was copied", includeJSON)
		}
		if string(output) != string(inputJSON) {
			t.Fatalf("normalized request changed:\n got: %s\nwant: %s", output, inputJSON)
		}
	}
}

func TestConvertOpenAIResponsesRequestToCodex_FiltersPromptCacheRetention(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.6-terra",
		"prompt_cache_retention": "24h",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "hello"
					}
				]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6-terra", inputJSON, true)
	if gjson.GetBytes(output, "prompt_cache_retention").Exists() {
		t.Fatalf("prompt_cache_retention should be removed: %s", string(output))
	}
}

// TestConvertSystemRoleToDeveloper_AssistantRole tests that assistant role is preserved
func TestConvertSystemRoleToDeveloper_AssistantRole(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [{"type": "input_text", "text": "You are helpful."}]
			},
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "Hello"}]
			},
			{
				"type": "message",
				"role": "assistant",
				"content": [{"type": "output_text", "text": "Hi!"}]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check system -> developer
	firstRole := gjson.Get(outputStr, "input.0.role")
	if firstRole.String() != "developer" {
		t.Errorf("Expected first role 'developer', got '%s'", firstRole.String())
	}

	// Check user unchanged
	secondRole := gjson.Get(outputStr, "input.1.role")
	if secondRole.String() != "user" {
		t.Errorf("Expected second role 'user', got '%s'", secondRole.String())
	}

	// Check assistant unchanged
	thirdRole := gjson.Get(outputStr, "input.2.role")
	if thirdRole.String() != "assistant" {
		t.Errorf("Expected third role 'assistant', got '%s'", thirdRole.String())
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesWebSearchPreview(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4-mini",
		"input": "find latest OpenAI model news",
		"tools": [
			{"type": "web_search_preview_2025_03_11"}
		],
		"tool_choice": {
			"type": "allowed_tools",
			"tools": [
				{"type": "web_search_preview"},
				{"type": "web_search_preview_2025_03_11"}
			]
		}
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)

	if got := gjson.GetBytes(output, "tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want %q: %s", got, "web_search", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.type").String(); got != "allowed_tools" {
		t.Fatalf("tool_choice.type = %q, want %q: %s", got, "allowed_tools", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.tools.0.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.0.type = %q, want %q: %s", got, "web_search", string(output))
	}
	if got := gjson.GetBytes(output, "tool_choice.tools.1.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.tools.1.type = %q, want %q: %s", got, "web_search", string(output))
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesTopLevelToolChoicePreviewAlias(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.4-mini",
		"input": "find latest OpenAI model news",
		"tool_choice": {"type": "web_search_preview_2025_03_11"}
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.4-mini", inputJSON, false)

	if got := gjson.GetBytes(output, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want %q: %s", got, "web_search", string(output))
	}
}

func TestUserFieldDeletion(t *testing.T) {
	inputJSON := []byte(`{  
		"model": "gpt-5.2",  
		"user": "test-user",  
		"input": [{"role": "user", "content": "Hello"}]  
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Verify user field is deleted
	userField := gjson.Get(outputStr, "user")
	if userField.Exists() {
		t.Errorf("user field should be deleted, but it was found with value: %s", userField.Raw)
	}
}

func TestContextManagementCompactionCompatibility(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"context_management": [
			{
				"type": "compaction",
				"compact_threshold": 12000
			}
		],
		"input": [{"role":"user","content":"hello"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if gjson.Get(outputStr, "context_management").Exists() {
		t.Fatalf("context_management should be removed for Codex compatibility")
	}
	if gjson.Get(outputStr, "truncation").Exists() {
		t.Fatalf("truncation should be removed for Codex compatibility")
	}
}

func TestTruncationRemovedForCodexCompatibility(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"truncation": "disabled",
		"input": [{"role":"user","content":"hello"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if gjson.Get(outputStr, "truncation").Exists() {
		t.Fatalf("truncation should be removed for Codex compatibility")
	}
}

func TestStripCodexResponsesCacheBreakpoints(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "Hello world",
						"prompt_cache_breakpoint": {"mode": "explicit"}
					},
					{
						"type": "input_text",
						"text": "Second part"
					}
				]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if strings.Contains(outputStr, "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint should not exist in the output JSON")
	}
	if gjson.Get(outputStr, "input.0.content.0.text").String() != "Hello world" {
		t.Fatalf("text content should be preserved")
	}
	if gjson.Get(outputStr, "input.0.content.1.text").String() != "Second part" {
		t.Fatalf("second content part should be preserved")
	}
}

func TestStripCodexResponsesCacheBreakpoints_FunctionCallOutputParts(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "function_call",
				"name": "shell",
				"call_id": "call_abc",
				"arguments": "{}"
			},
			{
				"type": "function_call_output",
				"call_id": "call_abc",
				"output": [
					{
						"type": "input_text",
						"text": "tool output",
						"prompt_cache_breakpoint": {"mode": "explicit"}
					}
				]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if strings.Contains(outputStr, "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint should not exist in the output JSON")
	}
	if gjson.Get(outputStr, "input.1.output.0.text").String() != "tool output" {
		t.Fatalf("function_call_output text should be preserved")
	}
}

func TestStripCodexResponsesCacheBreakpoints_ItemLevel(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": "hi"}],
				"prompt_cache_breakpoint": {"mode": "explicit"}
			},
			{
				"type": "function_call_output",
				"call_id": "call_abc",
				"output": "plain string output",
				"prompt_cache_breakpoint": {"mode": "explicit"}
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if strings.Contains(outputStr, "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint should not exist in the output JSON")
	}
	if gjson.Get(outputStr, "input.0.content.0.text").String() != "hi" {
		t.Fatalf("message content should be preserved")
	}
	if gjson.Get(outputStr, "input.1.output").String() != "plain string output" {
		t.Fatalf("string output should be preserved")
	}
}

func TestStripCodexResponsesCacheBreakpoints_CombinedItemAndPartLevel(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "function_call_output",
				"call_id": "call_123",
				"prompt_cache_breakpoint": {"mode": "explicit"},
				"output": [
					{
						"type": "input_text",
						"text": "result part 1",
						"prompt_cache_breakpoint": {"mode": "explicit"}
					},
					{
						"type": "input_text",
						"text": "result part 2"
					}
				]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if strings.Contains(outputStr, "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint should not exist in the output JSON")
	}
	if gjson.Get(outputStr, "input.#").Int() != 1 {
		t.Fatalf("expected 1 input item, got %d", gjson.Get(outputStr, "input.#").Int())
	}
	if gjson.Get(outputStr, "input.0.call_id").String() != "call_123" {
		t.Fatalf("call_id should be preserved")
	}
	if gjson.Get(outputStr, "input.0.output.0.text").String() != "result part 1" {
		t.Fatalf("output part 0 text should be preserved")
	}
	if gjson.Get(outputStr, "input.0.output.1.text").String() != "result part 2" {
		t.Fatalf("output part 1 text should be preserved")
	}
}

func TestStripCodexResponsesCacheBreakpoints_WithSystemRole(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{
				"type": "message",
				"role": "system",
				"content": [
					{
						"type": "input_text",
						"text": "System prompt",
						"prompt_cache_breakpoint": {"mode": "explicit"}
					}
				]
			},
			{
				"type": "message",
				"role": "user",
				"content": [
					{
						"type": "input_text",
						"text": "User query",
						"prompt_cache_breakpoint": {"mode": "explicit"}
					}
				]
			}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	// Check system role is converted to developer
	if gjson.Get(outputStr, "input.0.role").String() != "developer" {
		t.Fatalf("expected role 'developer', got %q", gjson.Get(outputStr, "input.0.role").String())
	}
	// Check prompt_cache_breakpoint is completely removed from payload
	if strings.Contains(outputStr, "prompt_cache_breakpoint") {
		t.Fatalf("prompt_cache_breakpoint should not exist in the output JSON")
	}
	if gjson.Get(outputStr, "input.0.content.0.text").String() != "System prompt" {
		t.Fatalf("expected system prompt text preserved, got %q", gjson.Get(outputStr, "input.0.content.0.text").String())
	}
	if gjson.Get(outputStr, "input.1.content.0.text").String() != "User query" {
		t.Fatalf("expected user query text preserved, got %q", gjson.Get(outputStr, "input.1.content.0.text").String())
	}
}

func BenchmarkConvertSystemRoleToDeveloperLargeInput(b *testing.B) {
	cases := []struct {
		name      string
		inputJSON []byte
	}{
		{
			name:      "200_input_1_system",
			inputJSON: makeLargeResponsesInputForBenchmark(200, 200),
		},
		{
			name:      "200_input_2_system",
			inputJSON: makeLargeResponsesInputForBenchmark(200, 100),
		},
		{
			name:      "2000_input_20_system",
			inputJSON: makeLargeResponsesInputForBenchmark(2000, 100),
		},
	}
	benchmarks := []struct {
		name string
		fn   func([]byte) []byte
	}{
		{
			name: "previous_root_path_rewrite",
			fn:   convertSystemRoleToDeveloperPreviousRootPathRewriteForBenchmark,
		},
		{
			name: "current_rebuilt_input_json_marshal",
			fn:   convertSystemRoleToDeveloper,
		},
	}

	for _, testCase := range cases {
		for _, benchmark := range benchmarks {
			b.Run(testCase.name+"/"+benchmark.name, func(b *testing.B) {
				output := benchmark.fn(testCase.inputJSON)
				if got := gjson.GetBytes(output, "input.0.role").String(); got != "developer" {
					b.Fatalf("input.0.role = %q, want %q", got, "developer")
				}
				if got := gjson.GetBytes(output, "input.1.role").String(); got != "user" {
					b.Fatalf("input.1.role = %q, want %q", got, "user")
				}

				b.ReportAllocs()
				b.SetBytes(int64(len(testCase.inputJSON)))
				b.ResetTimer()

				var benchmarkOutput []byte
				for i := 0; i < b.N; i++ {
					benchmarkOutput = benchmark.fn(testCase.inputJSON)
				}
				benchmarkConvertSystemRoleOutput = benchmarkOutput
			})
		}
	}
}

func BenchmarkConvertOpenAIResponsesRequestToCodexNormalizedPayload(b *testing.B) {
	cases := []struct {
		name      string
		inputJSON []byte
	}{
		{name: "1KiB", inputJSON: makeNormalizedResponsesRequestForBenchmark(1 << 10)},
		{name: "1MiB", inputJSON: makeNormalizedResponsesRequestForBenchmark(1 << 20)},
		{name: "8MiB", inputJSON: makeNormalizedResponsesRequestForBenchmark(8 << 20)},
	}

	for _, testCase := range cases {
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(testCase.inputJSON)))
			b.ResetTimer()

			var output []byte
			for b.Loop() {
				output = ConvertOpenAIResponsesRequestToCodex("gpt-5.6", testCase.inputJSON, true)
			}
			benchmarkConvertNormalizedOutput = output
		})
	}
}

func makeNormalizedResponsesRequestForBenchmark(contentBytes int) []byte {
	var builder strings.Builder
	builder.Grow(contentBytes + 256)
	builder.WriteString(`{"model":"gpt-5.6","stream":true,"store":false,"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":"`)
	builder.WriteString(strings.Repeat("x", contentBytes))
	builder.WriteString(`"}]}`)
	return []byte(builder.String())
}

func makeLargeResponsesInputForBenchmark(inputCount int, systemEvery int) []byte {
	var builder strings.Builder
	builder.Grow(inputCount * 96)
	builder.WriteString(`{"model":"gpt-5.2","input":[`)
	for i := 0; i < inputCount; i++ {
		if i > 0 {
			builder.WriteByte(',')
		}
		role := "user"
		if i%systemEvery == 0 {
			role = "system"
		}
		builder.WriteString(`{"type":"message","role":"`)
		builder.WriteString(role)
		builder.WriteString(`","content":[{"type":"input_text","text":"message `)
		builder.WriteString(strconv.Itoa(i))
		builder.WriteString(`"}]}`)
	}
	builder.WriteString(`]}`)
	return []byte(builder.String())
}

func convertSystemRoleToDeveloperPreviousRootPathRewriteForBenchmark(rawJSON []byte) []byte {
	inputResult := gjson.GetBytes(rawJSON, "input")
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputArray := inputResult.Array()
	result := rawJSON

	for i := 0; i < len(inputArray); i++ {
		rolePath := fmt.Sprintf("input.%d.role", i)
		if gjson.GetBytes(result, rolePath).String() == "system" {
			result, _ = sjson.SetBytes(result, rolePath, "developer")
		}
	}

	return result
}

func TestConvertOpenAIResponsesRequestToCodex_ServiceTier(t *testing.T) {
	tests := []struct {
		name       string
		tierJSON   string
		wantExists bool
		wantTier   string
	}{
		{
			name:       "priority preserved",
			tierJSON:   `"priority"`,
			wantExists: true,
			wantTier:   "priority",
		},
		{
			name:       "priority case insensitive and trimmed",
			tierJSON:   `" Priority "`,
			wantExists: true,
			wantTier:   "priority",
		},
		{
			name:       "fast normalized to priority",
			tierJSON:   `"fast"`,
			wantExists: true,
			wantTier:   "priority",
		},
		{
			name:       "ultrafast preserved",
			tierJSON:   `"ultrafast"`,
			wantExists: true,
			wantTier:   "ultrafast",
		},
		{
			name:       "ultrafast case insensitive and trimmed",
			tierJSON:   `" UltraFast "`,
			wantExists: true,
			wantTier:   "ultrafast",
		},
		{
			name:       "standard stripped",
			tierJSON:   `"standard"`,
			wantExists: false,
		},
		{
			name:       "default stripped",
			tierJSON:   `"default"`,
			wantExists: false,
		},
		{
			name:       "flex stripped",
			tierJSON:   `"flex"`,
			wantExists: false,
		},
		{
			name:       "non-string stripped",
			tierJSON:   `123`,
			wantExists: false,
		},
		{
			name:       "null stripped",
			tierJSON:   `null`,
			wantExists: false,
		},
		{
			name:       "bool stripped",
			tierJSON:   `true`,
			wantExists: false,
		},
		{
			name:       "empty string stripped",
			tierJSON:   `""`,
			wantExists: false,
		},
		{
			name:       "whitespace string stripped",
			tierJSON:   `"   "`,
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(fmt.Sprintf(`{"model":"gpt-5.6","service_tier":%s,"input":[{"type":"message","role":"user","content":"hello"}]}`, tt.tierJSON))
			output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)
			res := gjson.GetBytes(output, "service_tier")
			if res.Exists() != tt.wantExists {
				t.Fatalf("service_tier exists = %v, want %v; output: %s", res.Exists(), tt.wantExists, string(output))
			}
			if tt.wantExists && res.String() != tt.wantTier {
				t.Fatalf("service_tier = %q, want %q; output: %s", res.String(), tt.wantTier, string(output))
			}
		})
	}
}

// TestConvertOpenAIResponsesRequestToCodex_NormalizesEmptyFunctionCallArguments
// covers the parameter-less tool call shape that strict Codex Responses
// upstreams reject with "`arguments` must be valid JSON": only blank string
// arguments on function_call history items become "{}", everything else is
// preserved byte-for-byte in intent.
func TestConvertOpenAIResponsesRequestToCodex_NormalizesEmptyFunctionCallArguments(t *testing.T) {
	inputJSON := []byte(`{"model":"gpt-5.6","input":[
		{"type":"function_call","id":"fc_empty","call_id":"call_empty","name":"create_worktree","arguments":""},
		{"type":"function_call","id":"fc_blank","call_id":"call_blank","name":"list_artifacts","arguments":"   \t\n"},
		{"type":"function_call","id":"fc_kept","call_id":"call_kept","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
		{"type":"function_call","id":"fc_broken","call_id":"call_broken","name":"exec_command","arguments":"not-json"},
		{"type":"function_call","id":"fc_missing","call_id":"call_missing","name":"no_args"},
		{"type":"function_call","id":"fc_null","call_id":"call_null","name":"null_args","arguments":null},
		{"type":"function_call","id":"fc_num","call_id":"call_num","name":"num_args","arguments":123},
		{"type":"function_call_output","call_id":"call_empty","output":"worktree created"},
		{"type":"custom_tool_call","id":"ctc_1","call_id":"call_custom","name":"apply_patch","input":"exact patch"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
	]}`)
	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)
	for index, want := range []string{"{}", "{}", `{"cmd":"pwd"}`, "not-json"} {
		if got := gjson.GetBytes(output, "input."+strconv.Itoa(index)+".arguments").String(); got != want {
			t.Fatalf("input.%d.arguments = %q, want %q; output: %s", index, got, want, string(output))
		}
	}
	if gjson.GetBytes(output, "input.4.arguments").Exists() {
		t.Fatalf("input.4 (missing arguments) should not gain arguments field; output: %s", string(output))
	}
	if got := gjson.GetBytes(output, "input.5.arguments").Type; got != gjson.Null {
		t.Fatalf("input.5.arguments type = %v, want null; output: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "input.6.arguments").Int(); got != 123 {
		t.Fatalf("input.6.arguments = %d, want 123; output: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "input.7.type").String(); got != "function_call_output" || gjson.GetBytes(output, "input.7.output").String() != "worktree created" {
		t.Fatalf("input.7 should be function_call_output with unchanged output; output: %s", string(output))
	}
	if got := gjson.GetBytes(output, "input.8.type").String(); got != "custom_tool_call" {
		t.Fatalf("input.8.type = %q, want custom_tool_call; output: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "input.8.input").String(); got != "exact patch" {
		t.Fatalf("input.8.input = %q, want exact patch; output: %s", got, string(output))
	}
	if gjson.GetBytes(output, "input.8.arguments").Exists() {
		t.Fatalf("custom_tool_call must not gain arguments; output: %s", string(output))
	}
	if got := gjson.GetBytes(output, "input.9.type").String(); got != "message" {
		t.Fatalf("input.9.type = %q, want message; output: %s", got, string(output))
	}
}
