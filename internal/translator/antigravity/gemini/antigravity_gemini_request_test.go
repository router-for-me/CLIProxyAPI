package gemini

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertGeminiRequestToAntigravity_PreserveValidSignature(t *testing.T) {
	// Valid signature on functionCall should be preserved
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	inputJSON := []byte(fmt.Sprintf(`{
		"model": "gemini-3-pro-preview",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "test_tool", "args": {}}, "thoughtSignature": "%s"}
				]
			}
		]
	}`, validSignature))

	output := ConvertGeminiRequestToAntigravity("gemini-3-pro-preview", inputJSON, false)
	outputStr := string(output)

	// Check that valid thoughtSignature is preserved
	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part, got %d", len(parts))
	}

	sig := parts[0].Get("thoughtSignature").String()
	if sig != validSignature {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", validSignature, sig)
	}
}

func TestConvertGeminiRequestToAntigravity_AddSkipSentinelToFunctionCall(t *testing.T) {
	// functionCall without signature should get skip_thought_signature_validator
	inputJSON := []byte(`{
		"model": "gemini-3-pro-preview",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "test_tool", "args": {}}}
				]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("gemini-3-pro-preview", inputJSON, false)
	outputStr := string(output)

	// Check that skip_thought_signature_validator is added to functionCall
	sig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature").String()
	expectedSig := "skip_thought_signature_validator"
	if sig != expectedSig {
		t.Errorf("Expected skip sentinel '%s', got '%s'", expectedSig, sig)
	}
}

func TestConvertGeminiRequestToAntigravity_ParallelFunctionCalls(t *testing.T) {
	// Multiple functionCalls should all get skip_thought_signature_validator
	inputJSON := []byte(`{
		"model": "gemini-3-pro-preview",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "tool_one", "args": {"a": "1"}}},
					{"functionCall": {"name": "tool_two", "args": {"b": "2"}}}
				]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("gemini-3-pro-preview", inputJSON, false)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}

	expectedSig := "skip_thought_signature_validator"
	for i, part := range parts {
		sig := part.Get("thoughtSignature").String()
		if sig != expectedSig {
			t.Errorf("Part %d: Expected '%s', got '%s'", i, expectedSig, sig)
		}
	}
}

func TestFixCLIToolResponse_PreservesFunctionResponseParts(t *testing.T) {
	// When functionResponse contains a "parts" field with inlineData (from Claude
	// translator's image embedding), fixCLIToolResponse should preserve it as-is.
	// parseFunctionResponseRaw returns response.Raw for valid JSON objects,
	// so extra fields like "parts" survive the pipeline.
	input := `{
		"model": "claude-opus-4-6-thinking",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"functionCall": {"name": "screenshot", "args": {}}
						}
					]
				},
				{
					"role": "function",
					"parts": [
						{
							"functionResponse": {
								"id": "tool-001",
								"name": "screenshot",
								"response": {"result": "Screenshot taken"},
								"parts": [
									{"inlineData": {"mimeType": "image/png", "data": "iVBOR"}}
								]
							}
						}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	// Find the function response content (role=function)
	contents := gjson.Get(result, "request.contents").Array()
	var funcContent gjson.Result
	for _, c := range contents {
		if c.Get("role").String() == "function" {
			funcContent = c
			break
		}
	}
	if !funcContent.Exists() {
		t.Fatal("function role content should exist in output")
	}

	// The functionResponse should be preserved with its parts field
	funcResp := funcContent.Get("parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatal("functionResponse should exist in output")
	}

	// Verify the parts field with inlineData is preserved
	inlineParts := funcResp.Get("parts").Array()
	if len(inlineParts) != 1 {
		t.Fatalf("Expected 1 inlineData part in functionResponse.parts, got %d", len(inlineParts))
	}
	if inlineParts[0].Get("inlineData.mimeType").String() != "image/png" {
		t.Errorf("Expected mimeType 'image/png', got '%s'", inlineParts[0].Get("inlineData.mimeType").String())
	}
	if inlineParts[0].Get("inlineData.data").String() != "iVBOR" {
		t.Errorf("Expected data 'iVBOR', got '%s'", inlineParts[0].Get("inlineData.data").String())
	}

	// Verify response.result is also preserved
	if funcResp.Get("response.result").String() != "Screenshot taken" {
		t.Errorf("Expected response.result 'Screenshot taken', got '%s'", funcResp.Get("response.result").String())
	}
}
