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

func TestFixCLIToolResponse_BackfillsEmptyFunctionResponseName(t *testing.T) {
	// When the Amp client sends functionResponse with an empty name,
	// fixCLIToolResponse should backfill it from the corresponding functionCall.
	input := `{
		"model": "gemini-3-pro-preview",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Bash", "args": {"cmd": "ls"}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "", "response": {"output": "file1.txt"}}}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

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

	name := funcContent.Get("parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected backfilled name 'Bash', got '%s'", name)
	}
}

func TestFixCLIToolResponse_BackfillsMultipleEmptyNames(t *testing.T) {
	// Parallel function calls: both responses have empty names.
	input := `{
		"model": "gemini-3-pro-preview",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Read", "args": {"path": "/a"}}},
						{"functionCall": {"name": "Grep", "args": {"pattern": "x"}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "", "response": {"result": "content a"}}},
						{"functionResponse": {"name": "", "response": {"result": "match x"}}}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

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

	parts := funcContent.Get("parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 function response parts, got %d", len(parts))
	}

	name0 := parts[0].Get("functionResponse.name").String()
	name1 := parts[1].Get("functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first response name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second response name 'Grep', got '%s'", name1)
	}
}

func TestFixCLIToolResponse_PreservesExistingName(t *testing.T) {
	// When functionResponse already has a valid name, it should be preserved.
	input := `{
		"model": "gemini-3-pro-preview",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Bash", "args": {}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "Bash", "response": {"result": "ok"}}}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

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

	name := funcContent.Get("parts.0.functionResponse.name").String()
	if name != "Bash" {
		t.Errorf("Expected preserved name 'Bash', got '%s'", name)
	}
}

func TestFixCLIToolResponse_MoreResponsesThanCalls(t *testing.T) {
	// If there are more function responses than calls, unmatched extras are discarded by grouping.
	input := `{
		"model": "gemini-3-pro-preview",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Bash", "args": {}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "", "response": {"result": "ok"}}},
						{"functionResponse": {"name": "", "response": {"result": "extra"}}}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

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

	// First response should be backfilled from the call
	name0 := funcContent.Get("parts.0.functionResponse.name").String()
	if name0 != "Bash" {
		t.Errorf("Expected first response name 'Bash', got '%s'", name0)
	}
}

func TestFixCLIToolResponse_MultipleGroupsFIFO(t *testing.T) {
	// Two sequential function call groups should be matched FIFO.
	input := `{
		"model": "gemini-3-pro-preview",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Read", "args": {}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "", "response": {"result": "file content"}}}
					]
				},
				{
					"role": "model",
					"parts": [
						{"functionCall": {"name": "Grep", "args": {}}}
					]
				},
				{
					"role": "function",
					"parts": [
						{"functionResponse": {"name": "", "response": {"result": "match"}}}
					]
				}
			]
		}
	}`

	result, err := fixCLIToolResponse(input)
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.Get(result, "request.contents").Array()
	var funcContents []gjson.Result
	for _, c := range contents {
		if c.Get("role").String() == "function" {
			funcContents = append(funcContents, c)
		}
	}
	if len(funcContents) != 2 {
		t.Fatalf("Expected 2 function contents, got %d", len(funcContents))
	}

	name0 := funcContents[0].Get("parts.0.functionResponse.name").String()
	name1 := funcContents[1].Get("parts.0.functionResponse.name").String()
	if name0 != "Read" {
		t.Errorf("Expected first group name 'Read', got '%s'", name0)
	}
	if name1 != "Grep" {
		t.Errorf("Expected second group name 'Grep', got '%s'", name1)
	}
}
