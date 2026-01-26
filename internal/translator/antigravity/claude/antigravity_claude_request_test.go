package claude

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToAntigravity_BasicStructure(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Hello"}
				]
			}
		],
		"system": [
			{"type": "text", "text": "You are helpful"}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check model
	if gjson.Get(outputStr, "model").String() != "claude-sonnet-4-5" {
		t.Errorf("Expected model 'claude-sonnet-4-5', got '%s'", gjson.Get(outputStr, "model").String())
	}

	// Check contents exist
	contents := gjson.Get(outputStr, "request.contents")
	if !contents.Exists() || !contents.IsArray() {
		t.Error("request.contents should exist and be an array")
	}

	// Check role mapping (assistant -> model)
	firstContent := gjson.Get(outputStr, "request.contents.0")
	if firstContent.Get("role").String() != "user" {
		t.Errorf("Expected role 'user', got '%s'", firstContent.Get("role").String())
	}

	// Check systemInstruction
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Error("systemInstruction should exist")
	}
	if sysInstruction.Get("parts.0.text").String() != "You are helpful" {
		t.Error("systemInstruction text mismatch")
	}
}

func TestConvertClaudeRequestToAntigravity_RoleMapping(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Hi"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "Hello"}]}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// assistant should be mapped to model
	secondContent := gjson.Get(outputStr, "request.contents.1")
	if secondContent.Get("role").String() != "model" {
		t.Errorf("Expected role 'model' (mapped from 'assistant'), got '%s'", secondContent.Get("role").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingBlocks(t *testing.T) {
	cache.ClearSignatureCache("")

	// Valid signature must be at least 50 characters
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Let me think..."

	// Pre-cache the signature (simulating a previous response for the same thinking text)
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking block conversion (now in contents.1 due to user message)
	firstPart := gjson.Get(outputStr, "request.contents.1.parts.0")
	if !firstPart.Get("thought").Bool() {
		t.Error("thinking block should have thought: true")
	}
	if firstPart.Get("text").String() != thinkingText {
		t.Error("thinking text mismatch")
	}
	if firstPart.Get("thoughtSignature").String() != validSignature {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", validSignature, firstPart.Get("thoughtSignature").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingBlockWithoutSignature(t *testing.T) {
	cache.ClearSignatureCache("")

	// With cache enabled (default), unsigned thinking blocks should be removed
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Without signature, thinking block should be removed (not converted to text)
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (thinking removed), got %d", len(parts))
	}

	// Only text part should remain
	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed, not preserved")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ToolDeclarations(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [],
		"tools": [
			{
				"name": "test_tool",
				"description": "A test tool",
				"input_schema": {
					"type": "object",
					"properties": {
						"name": {"type": "string"}
					},
					"required": ["name"]
				}
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("gemini-1.5-pro", inputJSON, false)
	outputStr := string(output)

	// Check tools structure
	tools := gjson.Get(outputStr, "request.tools")
	if !tools.Exists() {
		t.Error("Tools should exist in output")
	}

	funcDecl := gjson.Get(outputStr, "request.tools.0.functionDeclarations.0")
	if funcDecl.Get("name").String() != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", funcDecl.Get("name").String())
	}

	// Check input_schema renamed to parametersJsonSchema
	if funcDecl.Get("parametersJsonSchema").Exists() {
		t.Log("parametersJsonSchema exists (expected)")
	}
	if funcDecl.Get("input_schema").Exists() {
		t.Error("input_schema should be removed")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUse(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "call_123",
						"name": "get_weather",
						"input": "{\"location\": \"Paris\"}"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Now we expect only 1 part (tool_use), no dummy thinking block injected
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (tool only, no dummy injection), got %d", len(parts))
	}

	// Check function call conversion at parts[0]
	funcCall := parts[0].Get("functionCall")
	if !funcCall.Exists() {
		t.Error("functionCall should exist at parts[0]")
	}
	if funcCall.Get("name").String() != "get_weather" {
		t.Errorf("Expected function name 'get_weather', got '%s'", funcCall.Get("name").String())
	}
	if funcCall.Get("id").String() != "call_123" {
		t.Errorf("Expected function id 'call_123', got '%s'", funcCall.Get("id").String())
	}
	// Verify skip_thought_signature_validator is added (bypass for tools without valid thinking)
	expectedSig := "skip_thought_signature_validator"
	actualSig := parts[0].Get("thoughtSignature").String()
	if actualSig != expectedSig {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", expectedSig, actualSig)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUse_WithSignature(t *testing.T) {
	cache.ClearSignatureCache("")

	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Let me think..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"},
					{
						"type": "tool_use",
						"id": "call_123",
						"name": "get_weather",
						"input": "{\"location\": \"Paris\"}"
					}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check function call has the signature from the preceding thinking block (now in contents.1)
	part := gjson.Get(outputStr, "request.contents.1.parts.1")
	if part.Get("functionCall.name").String() != "get_weather" {
		t.Errorf("Expected functionCall, got %s", part.Raw)
	}
	if part.Get("thoughtSignature").String() != validSignature {
		t.Errorf("Expected thoughtSignature '%s' on tool_use, got '%s'", validSignature, part.Get("thoughtSignature").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ReorderThinking(t *testing.T) {
	cache.ClearSignatureCache("")

	// Case: text block followed by thinking block -> should be reordered to thinking first
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Planning..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Test user message"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is the plan."},
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Verify order: Thinking block MUST be first (now in contents.1 due to user message)
	parts := gjson.Get(outputStr, "request.contents.1.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}

	if !parts[0].Get("thought").Bool() {
		t.Error("First part should be thinking block after reordering")
	}
	if parts[1].Get("text").String() != "Here is the plan." {
		t.Error("Second part should be text block")
	}
}

func TestConvertClaudeRequestToAntigravity_ToolResult(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "get_weather-call-123",
						"content": "22C sunny"
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check function response conversion
	funcResp := gjson.Get(outputStr, "request.contents.0.parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Error("functionResponse should exist")
	}
	if funcResp.Get("id").String() != "get_weather-call-123" {
		t.Errorf("Expected function id, got '%s'", funcResp.Get("id").String())
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingConfig(t *testing.T) {
	// Note: This test requires the model to be registered in the registry
	// with Thinking metadata. If the registry is not populated in test environment,
	// thinkingConfig won't be added. We'll test the basic structure only.
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [],
		"thinking": {
			"type": "enabled",
			"budget_tokens": 8000
		}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking config conversion (only if model supports thinking in registry)
	thinkingConfig := gjson.Get(outputStr, "request.generationConfig.thinkingConfig")
	if thinkingConfig.Exists() {
		if thinkingConfig.Get("thinkingBudget").Int() != 8000 {
			t.Errorf("Expected thinkingBudget 8000, got %d", thinkingConfig.Get("thinkingBudget").Int())
		}
		if !thinkingConfig.Get("includeThoughts").Bool() {
			t.Error("includeThoughts should be true")
		}
	} else {
		t.Log("thinkingConfig not present - model may not be registered in test registry")
	}
}

func TestConvertClaudeRequestToAntigravity_ImageContent(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUg=="
						}
					}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// Check inline data conversion
	inlineData := gjson.Get(outputStr, "request.contents.0.parts.0.inlineData")
	if !inlineData.Exists() {
		t.Error("inlineData should exist")
	}
	if inlineData.Get("mime_type").String() != "image/png" {
		t.Error("mime_type mismatch")
	}
	if !strings.Contains(inlineData.Get("data").String(), "iVBORw0KGgo") {
		t.Error("data mismatch")
	}
}

func TestConvertClaudeRequestToAntigravity_GenerationConfig(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-3-5-sonnet-20240620",
		"messages": [],
		"temperature": 0.7,
		"top_p": 0.9,
		"top_k": 40,
		"max_tokens": 2000
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	genConfig := gjson.Get(outputStr, "request.generationConfig")
	if genConfig.Get("temperature").Float() != 0.7 {
		t.Errorf("Expected temperature 0.7, got %f", genConfig.Get("temperature").Float())
	}
	if genConfig.Get("topP").Float() != 0.9 {
		t.Errorf("Expected topP 0.9, got %f", genConfig.Get("topP").Float())
	}
	if genConfig.Get("topK").Float() != 40 {
		t.Errorf("Expected topK 40, got %f", genConfig.Get("topK").Float())
	}
	if genConfig.Get("maxOutputTokens").Float() != 2000 {
		t.Errorf("Expected maxOutputTokens 2000, got %f", genConfig.Get("maxOutputTokens").Float())
	}
}

// ============================================================================
// Trailing Unsigned Thinking Block Removal
// ============================================================================

func TestConvertClaudeRequestToAntigravity_TrailingUnsignedThinking_Removed(t *testing.T) {
	// Last assistant message ends with unsigned thinking block - should be removed
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Hello"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is my answer"},
					{"type": "thinking", "thinking": "I should think more..."}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// The last part of the last assistant message should NOT be a thinking block
	lastMessageParts := gjson.Get(outputStr, "request.contents.1.parts")
	if !lastMessageParts.IsArray() {
		t.Fatal("Last message should have parts array")
	}
	parts := lastMessageParts.Array()
	if len(parts) == 0 {
		t.Fatal("Last message should have at least one part")
	}

	// The unsigned thinking should be removed, leaving only the text
	lastPart := parts[len(parts)-1]
	if lastPart.Get("thought").Bool() {
		t.Error("Trailing unsigned thinking block should be removed")
	}
}

func TestConvertClaudeRequestToAntigravity_TrailingSignedThinking_Kept(t *testing.T) {
	cache.ClearSignatureCache("")

	// Last assistant message ends with signed thinking block - should be kept
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	thinkingText := "Valid thinking..."

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "user",
				"content": [{"type": "text", "text": "Hello"}]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Here is my answer"},
					{"type": "thinking", "thinking": "` + thinkingText + `", "signature": "` + validSignature + `"}
				]
			}
		]
	}`)

	cache.CacheSignature("claude-sonnet-4-5-thinking", thinkingText, validSignature)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// The signed thinking block should be preserved
	lastMessageParts := gjson.Get(outputStr, "request.contents.1.parts")
	parts := lastMessageParts.Array()
	if len(parts) < 2 {
		t.Error("Signed thinking block should be preserved")
	}
}

func TestConvertClaudeRequestToAntigravity_MiddleUnsignedThinking_Removed(t *testing.T) {
	// Middle message has unsigned thinking - should be removed entirely
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Middle thinking..."},
					{"type": "text", "text": "Answer"}
				]
			},
			{
				"role": "user",
				"content": [{"type": "text", "text": "Follow up"}]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Unsigned thinking should be removed entirely
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part (thinking removed), got %d", len(parts))
	}

	// Only text part should remain
	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed, not preserved")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

// ============================================================================
// Tool + Thinking System Hint Injection
// ============================================================================

func TestConvertClaudeRequestToAntigravity_ToolAndThinking_HintInjected(t *testing.T) {
	// When both tools and thinking are enabled, hint should be injected into system instruction
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should contain the interleaved thinking hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Fatal("systemInstruction should exist")
	}

	// Check if hint is appended
	sysText := sysInstruction.Get("parts").Array()
	found := false
	for _, part := range sysText {
		if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Interleaved thinking hint should be injected when tools and thinking are both active, got: %v", sysInstruction.Raw)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolsOnly_NoHint(t *testing.T) {
	// When only tools are present (no thinking), hint should NOT be injected
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", inputJSON, false)
	outputStr := string(output)

	// System instruction should NOT contain the hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if sysInstruction.Exists() {
		for _, part := range sysInstruction.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
				t.Error("Hint should NOT be injected when only tools are present (no thinking)")
			}
		}
	}
}

func TestConvertClaudeRequestToAntigravity_ThinkingOnly_NoHint(t *testing.T) {
	// When only thinking is enabled (no tools), hint should NOT be injected
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"system": [{"type": "text", "text": "You are helpful."}],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should NOT contain the hint (no tools)
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if sysInstruction.Exists() {
		for _, part := range sysInstruction.Get("parts").Array() {
			if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
				t.Error("Hint should NOT be injected when only thinking is present (no tools)")
			}
		}
	}
}

func TestConvertClaudeRequestToAntigravity_ToolAndThinking_NoExistingSystem(t *testing.T) {
	// When tools + thinking but no system instruction, should create one with hint
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Hello"}]}],
		"tools": [
			{
				"name": "get_weather",
				"description": "Get weather",
				"input_schema": {"type": "object", "properties": {"location": {"type": "string"}}}
			}
		],
		"thinking": {"type": "enabled", "budget_tokens": 8000}
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// System instruction should be created with hint
	sysInstruction := gjson.Get(outputStr, "request.systemInstruction")
	if !sysInstruction.Exists() {
		t.Fatal("systemInstruction should be created when tools + thinking are active")
	}

	sysText := sysInstruction.Get("parts").Array()
	found := false
	for _, part := range sysText {
		if strings.Contains(part.Get("text").String(), "Interleaved thinking is enabled") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Interleaved thinking hint should be in created systemInstruction, got: %v", sysInstruction.Raw)
	}
}

// ============================================================================
// normalizeSignature Tests
// ============================================================================

func TestNormalizeSignature_CLIProxyAPIFormat(t *testing.T) {
	// "claude#..." format should extract the signature after "#"
	sig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	expected := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestNormalizeSignature_GeminiFormat(t *testing.T) {
	// "gemini#..." format should extract the signature after "#"
	sig := "gemini#R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	expected := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestNormalizeSignature_AnthropicFormat(t *testing.T) {
	// "E..." format (Anthropic direct, 1-layer Base64)
	// Should be Base64 encoded to 2-layer format (no prefix)
	sig := "EjIxMjM0NTY3ODkwYWJjZGVmMTIzNDU2Nzg5MGFiY2RlZjEyMzQ1Njc4OTBhYmNkZWYxMg=="

	result := normalizeSignature(sig)

	// Result should start with "R" (Base64 of "E" starts with "R")
	if !strings.HasPrefix(result, "R") {
		t.Errorf("Expected result to start with 'R', got '%s'", result)
	}
	// Encoded part should be longer than original (Base64 encoding adds ~33% overhead)
	if len(result) <= len(sig) {
		t.Errorf("Base64 encoded should be longer: input=%d, output=%d", len(sig), len(result))
	}
}

func TestNormalizeSignature_GoogleVertexFormat(t *testing.T) {
	// "R..." format (Google Vertex, 2-layer Base64)
	// Should return as-is (already correct format)
	sig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	result := normalizeSignature(sig)

	if result != sig {
		t.Errorf("Expected '%s', got '%s'", sig, result)
	}
}

func TestNormalizeSignature_UnknownFormat(t *testing.T) {
	// Unknown format should be returned as-is (let original logic handle it)
	testCases := []string{
		"X1234567890",
		"1234567890",
		"abc",
	}

	for _, sig := range testCases {
		result := normalizeSignature(sig)
		if result != sig {
			t.Errorf("Expected '%s' (same as input), got '%s'", sig, result)
		}
	}
}

func TestNormalizeSignature_EmptyString(t *testing.T) {
	// Empty string should return empty
	result := normalizeSignature("")
	if result != "" {
		t.Errorf("Expected empty for empty input, got '%s'", result)
	}
}

func TestNormalizeSignature_CLIProxyAPIFormat_EmptyAfterPrefix(t *testing.T) {
	// "claude#" with nothing after should extract empty string
	sig := "claude#"
	result := normalizeSignature(sig)

	if result != "" {
		t.Errorf("Expected empty string, got '%s'", result)
	}
}

func TestNormalizeSignature_RoundTrip_AnthropicToGoogle(t *testing.T) {
	// Simulate: Anthropic returns E... -> we encode -> result starts with R
	anthropicSig := "EtgCCkgICxACGAIqQGF8Wm8HDPN/PnZe6Mv5SGcFreSRLSo8/i5qfxfx7dOxRoZGOQ"

	result := normalizeSignature(anthropicSig)

	// Result should start with R (base64 of 'E' = 0x45 = 69)
	// base64("E") = "RQ==" so base64("Etg...") starts with "R"
	if !strings.HasPrefix(result, "R") {
		t.Errorf("Encoded signature should start with 'R', got prefix '%c'", result[0])
	}
}

func TestConvertClaudeRequestToAntigravity_SignaturePassthrough(t *testing.T) {
	cache.ClearSignatureCache("")

	// Test that signature is correctly passed through to the output
	validSig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think...", "signature": "` + validSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Check thinking block has thoughtSignature
	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	// Should be the part after "claude#"
	expectedSig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"
	if thoughtSig != expectedSig {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", expectedSig, thoughtSig)
	}
}

func TestConvertClaudeRequestToAntigravity_AnthropicSignatureFormat(t *testing.T) {
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	anthropicSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + anthropicSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	if !strings.HasPrefix(thoughtSig, "R") {
		t.Errorf("Expected thoughtSignature to start with 'R' (base64 of E...), got '%s'", thoughtSig)
	}

	if strings.Contains(thoughtSig, "#") {
		t.Errorf("thoughtSignature should not contain '#', got '%s'", thoughtSig)
	}
}

func TestConvertClaudeRequestToAntigravity_ToolUseInheritsSignature(t *testing.T) {
	// Test that tool_use inherits signature from preceding thinking block
	validSig := "claude#R1234567890abcdef1234567890abcdef1234567890abcdef12"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me use a tool...", "signature": "` + validSig + `"},
					{"type": "tool_use", "id": "tool_123", "name": "test_tool", "input": {"arg": "value"}}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	// Both thinking and tool_use should have the same signature
	thinkingSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()
	toolSig := gjson.Get(outputStr, "request.contents.0.parts.1.thoughtSignature").String()

	expectedSig := "R1234567890abcdef1234567890abcdef1234567890abcdef12"

	if thinkingSig != expectedSig {
		t.Errorf("Thinking thoughtSignature expected '%s', got '%s'", expectedSig, thinkingSig)
	}

	if toolSig != expectedSig {
		t.Errorf("Tool thoughtSignature expected '%s', got '%s'", expectedSig, toolSig)
	}
}

// ============================================================================
// SignatureCacheEnabled Toggle Tests
// ============================================================================

func TestSignatureCacheEnabled_DefaultTrue(t *testing.T) {
	if !cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be true by default")
	}
}

func TestSignatureCacheEnabled_Toggle(t *testing.T) {
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)
	if cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be false after SetSignatureCacheEnabled(false)")
	}

	cache.SetSignatureCacheEnabled(true)
	if !cache.SignatureCacheEnabled() {
		t.Error("SignatureCacheEnabled should be true after SetSignatureCacheEnabled(true)")
	}
}

func TestConvertClaudeRequestToAntigravity_CacheEnabled_DropsUnsigned(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(true)

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With cache enabled, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("thought").Bool() {
		t.Error("Thinking block should be removed when unsigned and cache enabled")
	}
	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_NormalizesAnthropicSignature(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	// Valid Claude signature with 0x12 first byte
	anthropicSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + anthropicSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	thoughtSig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()

	if !strings.HasPrefix(thoughtSig, "R") {
		t.Errorf("Expected thoughtSignature to start with 'R' (base64 of E...), got '%s'", thoughtSig)
	}

	if strings.Contains(thoughtSig, "#") {
		t.Errorf("thoughtSignature should not contain '#', got '%s'", thoughtSig)
	}
}

// ============================================================================
// isValidClaudeSignature Tests
// ============================================================================

func TestIsValidClaudeSignature_ValidAnthropicMaxFormat(t *testing.T) {
	// Valid Max subscription: 0x12 prefix, Channel=12
	sig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Anthropic Max signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidAnthropicAPIFormat(t *testing.T) {
	// Valid API: 0x12 prefix, Channel=11
	sig := "EoUICkYICxgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Anthropic API signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidAWSBedrockFormat(t *testing.T) {
	// Valid AWS Bedrock: 0x12 prefix, Channel=11, Field2=1
	sig := "Et0DCkgICxABGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid AWS Bedrock signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidGoogleVertexFormat(t *testing.T) {
	// Valid Google Vertex: 0x12 prefix, Channel=11, Field2=2
	sig := "EpUMCkgICxACGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	if !isValidClaudeSignature(sig) {
		t.Error("Valid Google Vertex 1-layer signature should pass validation")
	}
}

func TestIsValidClaudeSignature_ValidVertexTwoLayerFormat(t *testing.T) {
	// Valid 2-layer: R... that decodes to E... which decodes to 0x12 prefix
	innerSig := "EpUMCkgICxACGAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="
	twoLayerSig := base64.StdEncoding.EncodeToString([]byte(innerSig))

	if !strings.HasPrefix(twoLayerSig, "R") {
		t.Fatalf("Test setup error: 2-layer signature should start with R, got %c", twoLayerSig[0])
	}

	if !isValidClaudeSignature(twoLayerSig) {
		t.Error("Valid 2-layer Vertex signature should pass validation")
	}
}

func TestIsValidClaudeSignature_WithCLIProxyAPIPrefix(t *testing.T) {
	// Valid signature with "claude#" prefix
	innerSig := "EoYDCkYIDBgCAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	sig := "claude#" + innerSig

	if !isValidClaudeSignature(sig) {
		t.Error("Valid signature with 'claude#' prefix should pass validation")
	}
}

func TestIsValidClaudeSignature_InvalidEmptyString(t *testing.T) {
	if isValidClaudeSignature("") {
		t.Error("Empty signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidTooShort(t *testing.T) {
	if isValidClaudeSignature("EoAB") {
		t.Error("Short signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidWrongPrefix(t *testing.T) {
	// Does not start with E or R
	sig := "XoYDCkYIDBgCKkCTiMT2RGJPOfSxH3FmzDTRRwVQ5C2l8QHba5Ukg"
	if isValidClaudeSignature(sig) {
		t.Error("Signature with wrong prefix should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidProtobufStructure(t *testing.T) {
	// Valid Base64 but wrong first byte (not 0x12)
	// base64("ABC...") = first byte is 'A' = 0x41, not 0x12
	sig := "QUJDREVGMTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MA=="
	if isValidClaudeSignature(sig) {
		t.Error("Signature with wrong Protobuf structure should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidBase64(t *testing.T) {
	// E prefix but invalid Base64 content
	sig := "E!!!invalidbase64!!!1234567890123456789012345678901234567890"
	if isValidClaudeSignature(sig) {
		t.Error("Invalid Base64 signature should fail validation")
	}
}

func TestIsValidClaudeSignature_InvalidTwoLayerFirstLayerNotE(t *testing.T) {
	// R... but first layer doesn't decode to E...
	innerContent := "XYZnotEformat123456789012345678901234567890123456"
	twoLayerSig := base64.StdEncoding.EncodeToString([]byte(innerContent))

	if isValidClaudeSignature(twoLayerSig) {
		t.Error("2-layer signature with invalid inner format should fail validation")
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_InvalidSignatureDropsThinking(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	invalidSig := "invalid_signature"

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Thinking...", "signature": "` + invalidSig + `"},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With invalid signature in bypass mode, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}

func TestConvertClaudeRequestToAntigravity_CacheDisabled_UnsignedDropsThinking(t *testing.T) {
	cache.ClearSignatureCache("")
	original := cache.SignatureCacheEnabled()
	defer cache.SetSignatureCacheEnabled(original)

	cache.SetSignatureCacheEnabled(false)

	inputJSON := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think..."},
					{"type": "text", "text": "Answer"}
				]
			}
		]
	}`)

	output := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5-thinking", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("With no signature in bypass mode, expected 1 part (thinking dropped), got %d", len(parts))
	}

	if parts[0].Get("text").String() != "Answer" {
		t.Errorf("Expected text 'Answer', got '%s'", parts[0].Get("text").String())
	}
}
