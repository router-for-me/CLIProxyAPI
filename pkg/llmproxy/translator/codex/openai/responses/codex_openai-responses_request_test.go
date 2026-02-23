package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

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

func TestConvertOpenAIResponsesRequestToCodex_RemovesItemReferenceInputItems(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"input": [
			{"type": "item_reference", "id": "msg_123"},
			{"type": "message", "role": "user", "content": "hello"},
			{"type": "item_reference", "id": "msg_456"}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	input := gjson.Get(outputStr, "input")
	if !input.IsArray() {
		t.Fatalf("expected input to be an array")
	}
	if got := len(input.Array()); got != 1 {
		t.Fatalf("expected 1 input item after filtering item_reference, got %d", got)
	}
	if itemType := gjson.Get(outputStr, "input.0.type").String(); itemType != "message" {
		t.Fatalf("expected remaining input[0].type message, got %s", itemType)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_UsesVariantAsReasoningEffortFallback(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"variant": "high",
		"input": [
			{"type": "message", "role": "user", "content": "hello"}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "reasoning.effort").String(); got != "high" {
		t.Fatalf("expected reasoning.effort=high fallback, got %s", got)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_CPB0228_InputStringNormalizedToInputList(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5-codex",
		"input": "Summarize this request",
		"stream": false
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5-codex", inputJSON, false)
	outputStr := string(output)

	input := gjson.Get(outputStr, "input")
	if !input.IsArray() {
		t.Fatalf("expected input to be normalized to an array, got %s", input.Type.String())
	}
	if got := len(input.Array()); got != 1 {
		t.Fatalf("expected one normalized input message, got %d", got)
	}
	if got := gjson.Get(outputStr, "input.0.type").String(); got != "message" {
		t.Fatalf("expected input.0.type=message, got %q", got)
	}
	if got := gjson.Get(outputStr, "input.0.role").String(); got != "user" {
		t.Fatalf("expected input.0.role=user, got %q", got)
	}
	if got := gjson.Get(outputStr, "input.0.content.0.type").String(); got != "input_text" {
		t.Fatalf("expected input.0.content.0.type=input_text, got %q", got)
	}
	if got := gjson.Get(outputStr, "input.0.content.0.text").String(); got != "Summarize this request" {
		t.Fatalf("expected input text preserved, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_CPB0228_PreservesCompactionFieldsWithStringInput(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5-codex",
		"input": "continue",
		"previous_response_id": "resp_prev_1",
		"prompt_cache_key": "cache_abc",
		"safety_identifier": "safe_123"
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5-codex", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "previous_response_id").String(); got != "resp_prev_1" {
		t.Fatalf("expected previous_response_id to be preserved, got %q", got)
	}
	if got := gjson.Get(outputStr, "prompt_cache_key").String(); got != "cache_abc" {
		t.Fatalf("expected prompt_cache_key to be preserved, got %q", got)
	}
	if got := gjson.Get(outputStr, "safety_identifier").String(); got != "safe_123" {
		t.Fatalf("expected safety_identifier to be preserved, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_CPB0225_ConversationIDAliasMapsToPreviousResponseID(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5-codex",
		"input": "continue",
		"conversation_id": "resp_alias_1"
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5-codex", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "previous_response_id").String(); got != "resp_alias_1" {
		t.Fatalf("expected conversation_id alias to map to previous_response_id, got %q", got)
	}
	if gjson.Get(outputStr, "conversation_id").Exists() {
		t.Fatalf("expected conversation_id alias to be removed after normalization")
	}
}

func TestConvertOpenAIResponsesRequestToCodex_CPB0225_PrefersPreviousResponseIDOverAlias(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5-codex",
		"input": "continue",
		"previous_response_id": "resp_primary",
		"conversation_id": "resp_alias"
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5-codex", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "previous_response_id").String(); got != "resp_primary" {
		t.Fatalf("expected previous_response_id to win over conversation_id alias, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_UsesReasoningEffortOverVariant(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"reasoning": {"effort": "low"},
		"variant": "high",
		"input": [
			{"type": "message", "role": "user", "content": "hello"}
		]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if got := gjson.Get(outputStr, "reasoning.effort").String(); got != "low" {
		t.Fatalf("expected reasoning.effort to prefer explicit reasoning.effort low, got %s", got)
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesToolChoiceFunctionProxyPrefix(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"tools": [
			{
				"type": "function",
				"function": {"name": "send_email", "description": "send email", "parameters": {}}
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {"name": "proxy_send_email"}
		},
		"input": [{"type":"message","role":"user","content":"send email"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	if gjson.Get(outputStr, "tool_choice.function.name").String() != "send_email" {
		t.Fatalf("expected tool_choice.function.name to normalize to send_email, got %q", gjson.Get(outputStr, "tool_choice.function.name").String())
	}
	if gjson.Get(outputStr, "tools.0.function.name").String() != "send_email" {
		t.Fatalf("expected tools.0.function.name to normalize to send_email, got %q", gjson.Get(outputStr, "tools.0.function.name").String())
	}
}

func TestConvertOpenAIResponsesRequestToCodex_NormalizesToolsAndChoiceIndependently(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.2",
		"tools": [
			{
				"type": "function",
				"function": {"name": "` + longName(0) + `", "description": "x", "parameters": {}}
			},
			{
				"type": "function",
				"function": {"name": "` + longName(1) + `", "description": "y", "parameters": {}}
			}
		],
		"tool_choice": {
			"type": "function",
			"function": {"name": "proxy_` + longName(1) + `"}
		},
		"input": [{"type":"message","role":"user","content":"run"}]
	}`)

	output := ConvertOpenAIResponsesRequestToCodex("gpt-5.2", inputJSON, false)
	outputStr := string(output)

	t1 := gjson.Get(outputStr, "tools.0.function.name").String()
	t2 := gjson.Get(outputStr, "tools.1.function.name").String()
	tc := gjson.Get(outputStr, "tool_choice.function.name").String()

	if t1 == "" || t2 == "" || tc == "" {
		t.Fatalf("expected normalized names, got tool1=%q tool2=%q tool_choice=%q", t1, t2, tc)
	}
	if len(t1) > 64 || len(t2) > 64 || len(tc) > 64 {
		t.Fatalf("expected all normalized names <=64, got len(tool1)=%d len(tool2)=%d len(tool_choice)=%d", len(t1), len(t2), len(tc))
	}
}

func longName(i int) string {
	base := "proxy_mcp__very_long_prefix_segment_for_tool_normalization_"
	return base + strings.Repeat("x", 80) + string(rune('a'+i))
}
