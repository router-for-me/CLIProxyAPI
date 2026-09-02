package gemini

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConvertGeminiRequestToAntigravity_ReplacesClientSignatureOnFunctionCall(t *testing.T) {
	// Client signatures on Gemini function calls are not portable to Antigravity.
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

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	if len(parts) != 1 {
		t.Fatalf("Expected 1 part, got %d", len(parts))
	}

	sig := parts[0].Get("thoughtSignature").String()
	expectedSig := "skip_thought_signature_validator"
	if sig != expectedSig {
		t.Errorf("Expected thoughtSignature '%s', got '%s'", expectedSig, sig)
	}
}

func TestConvertGeminiRequestToAntigravity_DropsIncompatibleClientSignatureOnTextPart(t *testing.T) {
	validSignature := "abc123validSignature1234567890123456789012345678901234567890"
	inputJSON := []byte(fmt.Sprintf(`{
		"model": "gemini-3-pro-preview",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "previous answer", "thoughtSignature": "%s"}
				]
			}
		]
	}`, validSignature))

	output := ConvertGeminiRequestToAntigravity("gemini-3-pro-preview", inputJSON, false)
	if signature := gjson.GetBytes(output, "request.contents.0.parts.0.thoughtSignature"); signature.Exists() {
		t.Fatalf("incompatible text signature should be dropped, got %s", signature.Raw)
	}
}

func TestConvertGeminiRequestToAntigravity_LeavesUnsignedThoughtPartUnsigned(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3-pro-preview",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"thought": "internal reasoning"}
				]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("gemini-3-pro-preview", inputJSON, false)
	if signature := gjson.GetBytes(output, "request.contents.0.parts.0.thoughtSignature"); signature.Exists() {
		t.Fatalf("unsigned thought should remain unsigned, got %s", signature.Raw)
	}
}

func TestConvertGeminiRequestToAntigravity_SkipsUppercaseClaudeModel(t *testing.T) {
	inputJSON := []byte(`{
		"model": "Claude-Test",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "test_tool", "args": {}}}
				]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("Claude-Test", inputJSON, false)
	outputStr := string(output)

	if sig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature"); sig.Exists() {
		t.Fatalf("Expected no thoughtSignature for Claude model, got %s", sig.Raw)
	}
}

func TestConvertGeminiRequestToAntigravity_ClaudeModelNormalizesStrictClaudeThoughtSignature(t *testing.T) {
	nativeSig := testAntigravityGeminiClaudeSignature(t)
	expectedSig, ok := signature.CompatibleAntigravityClaudeThinkingSignature(nativeSig)
	if !ok {
		t.Fatal("test Claude signature should be compatible with Antigravity Claude")
	}

	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "internal reasoning", "thought": true, "thoughtSignature": "` + nativeSig + `"},
					{"text": "visible answer"}
				]
			},
			{
				"role": "user",
				"parts": [{"text": "continue"}]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)

	part := gjson.GetBytes(output, "request.contents.0.parts.0")
	if !part.Get("thought").Bool() {
		t.Fatalf("first part should remain thought. Output: %s", output)
	}
	if got := part.Get("thoughtSignature").String(); got != expectedSig {
		t.Fatalf("thoughtSignature = %q, want %q. Output: %s", got, expectedSig, output)
	}
}

func TestConvertGeminiRequestToAntigravity_ClaudeModelDropsNonStrictEPrefixThoughtSignature(t *testing.T) {
	looseEPrefix := base64.StdEncoding.EncodeToString([]byte{0x12, 0x01, 0x02})
	if looseEPrefix[0] != 'E' {
		t.Fatalf("test signature should start with E, got %q", looseEPrefix[:1])
	}

	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "must not reach Claude", "thought": true, "thoughtSignature": "` + looseEPrefix + `"},
					{"text": "visible answer"}
				]
			},
			{
				"role": "user",
				"parts": [{"text": "continue"}]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)

	if gjson.GetBytes(output, `request.contents.#.parts.#(thought=true)#`).Int() != 0 {
		t.Fatalf("non-strict E-prefix thought block should be dropped. Output: %s", output)
	}
	if got := gjson.GetBytes(output, "request.contents.0.parts.0.text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q, want visible answer. Output: %s", got, output)
	}
}

func TestConvertGeminiRequestToAntigravity_ClaudeModelDropsEmptyThoughtText(t *testing.T) {
	nativeSig := testAntigravityGeminiClaudeSignature(t)
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "", "thought": true, "thoughtSignature": "` + nativeSig + `"},
					{"text": "visible answer"}
				]
			},
			{
				"role": "user",
				"parts": [{"text": "continue"}]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)

	if gjson.GetBytes(output, `request.contents.#.parts.#(thought=true)#`).Int() != 0 {
		t.Fatalf("empty-text thought block should be dropped for Antigravity Claude. Output: %s", output)
	}
	if got := gjson.GetBytes(output, "request.contents.0.parts.0.text").String(); got != "visible answer" {
		t.Fatalf("visible text = %q, want visible answer. Output: %s", got, output)
	}
}

func TestConvertGeminiRequestToAntigravity_ClaudeModelStripsUnneededFunctionCallSignature(t *testing.T) {
	nativeSig := testAntigravityGeminiClaudeSignature(t)
	inputJSON := []byte(`{
		"model": "claude-opus-4-6-thinking",
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "test_tool", "args": {}}, "thoughtSignature": "` + nativeSig + `"}
				]
			}
		]
	}`)

	output := ConvertGeminiRequestToAntigravity("claude-opus-4-6-thinking", inputJSON, false)

	part := gjson.GetBytes(output, "request.contents.0.parts.0")
	if !part.Get("functionCall").Exists() {
		t.Fatalf("functionCall should be preserved. Output: %s", output)
	}
	if part.Get("thoughtSignature").Exists() {
		t.Fatalf("functionCall thoughtSignature should be stripped for Claude target. Output: %s", output)
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

func testAntigravityGeminiClaudeSignature(t *testing.T) string {
	t.Helper()
	channelBlock := []byte{}
	channelBlock = protowire.AppendTag(channelBlock, 1, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 12)
	channelBlock = protowire.AppendTag(channelBlock, 2, protowire.VarintType)
	channelBlock = protowire.AppendVarint(channelBlock, 2)
	channelBlock = protowire.AppendTag(channelBlock, 6, protowire.BytesType)
	channelBlock = protowire.AppendString(channelBlock, "claude-sonnet-4-6")

	container := []byte{}
	container = protowire.AppendTag(container, 1, protowire.BytesType)
	container = protowire.AppendBytes(container, channelBlock)

	payload := []byte{}
	payload = protowire.AppendTag(payload, 2, protowire.BytesType)
	payload = protowire.AppendBytes(payload, container)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	return base64.StdEncoding.EncodeToString(payload)
}

func TestConvertGeminiRequestToAntigravity_ParallelFunctionCallsOnlyFirstGetsSentinel(t *testing.T) {
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
	parts := gjson.GetBytes(output, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("Expected 2 parts, got %d", len(parts))
	}
	if got := parts[0].Get("thoughtSignature").String(); got != signature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first call signature = %q, want sentinel", got)
	}
	if parts[1].Get("thoughtSignature").Exists() {
		t.Fatalf("second parallel call should remain unsigned: %s", parts[1].Raw)
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	// Find the function response content (role=function)
	contents := gjson.GetBytes(result, "request.contents").Array()
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
	// Empty functionResponse names are backfilled from the corresponding functionCall.
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.GetBytes(result, "request.contents").Array()
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.GetBytes(result, "request.contents").Array()
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.GetBytes(result, "request.contents").Array()
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.GetBytes(result, "request.contents").Array()
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

	result, err := fixCLIToolResponse([]byte(input))
	if err != nil {
		t.Fatalf("fixCLIToolResponse failed: %v", err)
	}

	contents := gjson.GetBytes(result, "request.contents").Array()
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

func TestConvertGeminiRequestToAntigravityDeduplicatesRequestWideAndDisambiguatesTools(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	inputJSON := []byte(`{
		"contents":[
			{"role":"model","parts":[{"functionCall":{"name":"` + second + `","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"` + second + `","response":{}}}]}
		],
		"tools":[
			{"functionDeclarations":[
				{"name":"lookup","parameters":{"type":"object"}},
				{"name":"` + first + `","parameters":{"type":"object"}}
			]},
			{"function_declarations":[
				{"name":"lookup","parameters":{"type":"object"}},
				{"name":"` + second + `","parameters":{"type":"object"}}
			]},
			{"functionDeclarations":[{"name":"lookup","parameters":{"type":"object"}}]}
		],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["` + second + `"]}}
	}`)

	out := ConvertGeminiRequestToAntigravity("gemini-3-flash", inputJSON, false)
	if got := len(gjson.GetBytes(out, "request.tools").Array()); got != 2 {
		t.Fatalf("tool count = %d, want 2 after removing the empty duplicate node. Output: %s", got, out)
	}
	camel := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
	snake := gjson.GetBytes(out, "request.tools.1.function_declarations").Array()
	if len(camel)+len(snake) != 3 {
		t.Fatalf("declaration count = %d, want 3. Output: %s", len(camel)+len(snake), out)
	}
	if len(camel) != 2 || len(snake) != 1 {
		t.Fatalf("declaration distribution = %d/%d, want 2/1. Output: %s", len(camel), len(snake), out)
	}
	firstMapped := camel[1].Get("name").String()
	secondMapped := snake[0].Get("name").String()
	if firstMapped == secondMapped || len(secondMapped) > 64 {
		t.Fatalf("collision names = %q and %q, want distinct names <= 64 chars", firstMapped, secondMapped)
	}
	if !camel[0].Get("parametersJsonSchema").Exists() || !snake[0].Get("parametersJsonSchema").Exists() {
		t.Fatalf("parameters were not normalized. Output: %s", out)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall.name").String(); got != secondMapped {
		t.Fatalf("functionCall.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse.name").String(); got != secondMapped {
		t.Fatalf("functionResponse.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != secondMapped {
		t.Fatalf("allowedFunctionNames.0 = %q, want %q. Output: %s", got, secondMapped, out)
	}
}

func TestConvertGeminiRequestToAntigravityMapsSnakeCaseFunctionReferences(t *testing.T) {
	inputJSON := []byte(`{
		"contents":[
			{"role":"model","parts":[{"function_call":{"name":"read_file","args":{}}}]},
			{"role":"user","parts":[{"function_response":{"name":"read_file","response":{}}}]}
		],
		"tools":[{"function_declarations":[{"name":"read/file"},{"name":"read_file"}]}],
		"tool_config":{"function_calling_config":{"allowed_function_names":["read_file"]}}
	}`)

	out := ConvertGeminiRequestToAntigravity("gemini-3-flash", inputJSON, false)
	mapped := gjson.GetBytes(out, "request.tools.0.function_declarations.1.name").String()
	if mapped == "" {
		t.Fatalf("mapped declaration name is empty. Output: %s", out)
	}
	for _, path := range []string{
		"request.contents.0.parts.0.function_call.name",
		"request.contents.1.parts.0.function_response.name",
		"request.tool_config.function_calling_config.allowed_function_names.0",
	} {
		if got := gjson.GetBytes(out, path).String(); got != mapped {
			t.Fatalf("%s = %q, want %q. Output: %s", path, got, mapped, out)
		}
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_PreservesNumberPrecision(t *testing.T) {
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"text": "thinking",
							"thought": true,
							"thoughtSignature": "invalid"
						},
						{
							"functionCall": {
								"name": "calc",
								"args": {
									"n": 12345678901234567890,
									"big": 9007199254740993
								}
							}
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	outputStr := string(output)

	bigVal := gjson.Get(outputStr, "request.contents.0.parts.0.functionCall.args.big").Raw
	nVal := gjson.Get(outputStr, "request.contents.0.parts.0.functionCall.args.n").Raw

	if bigVal != "9007199254740993" {
		t.Errorf("Precision lost for big: got %s, want 9007199254740993", bigVal)
	}
	if nVal != "12345678901234567890" {
		t.Errorf("Precision lost for n: got %s, want 12345678901234567890", nVal)
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_StripsFunctionCallSignatureForClaudeModel(t *testing.T) {
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"functionCall": {
								"name": "calc",
								"args": {}
							},
							"thoughtSignature": "skip_thought_signature_validator"
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	outputStr := string(output)

	sig := gjson.Get(outputStr, "request.contents.0.parts.0.thoughtSignature")
	if sig.Exists() {
		t.Fatalf("expected functionCall thoughtSignature to be stripped for Claude target model, got %s", sig.Raw)
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_StrictTypeChecks(t *testing.T) {
	// Non-boolean thought (e.g. "true" as string) and non-string text (e.g. 123) should not be treated as valid thinking block
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"text": "reasoning",
							"thought": "true",
							"thoughtSignature": "valid_signature_1234567890123456789012345678901234567890"
						},
						{
							"text": 123,
							"thought": true,
							"thoughtSignature": "valid_signature_1234567890123456789012345678901234567890"
						},
						{
							"text": "valid answer"
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	outputStr := string(output)

	parts := gjson.Get(outputStr, "request.contents.0.parts").Array()
	for i, part := range parts {
		if sig := part.Get("thoughtSignature"); sig.Exists() {
			t.Fatalf("part %d should not retain thoughtSignature, got %s", i, sig.Raw)
		}
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_StripsDuplicateSignatureKeys(t *testing.T) {
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"functionCall": {
								"name": "calc",
								"args": {}
							},
							"thoughtSignature": "first_signature",
							"thoughtSignature": "second_signature"
						},
						{
							"functionCall": {"name": "first"},
							"functionCall": {
								"name": "second",
								"thoughtSignature": "secret"
							}
						},
						{
							"functionCall": {
								"name": "first",
								"thoughtSignature": "secret"
							},
							"functionCall": {"name": "second"}
						},
						{
							"thoughtSignature": null,
							"thoughtSignature": "second_sig",
							"text": "regular answer"
						},
						{
							"thoughtSignature": "secret",
							"thoughtSignature": null,
							"text": "regular answer 2"
						},
						{
							"thoughtSignature": {"nested": "obj"},
							"text": "object sig"
						},
						{
							"thoughtSignature": [1, 2, 3],
							"text": "array sig"
						},
						{
							"functionCall": {"thought\u0053ignature": "secret"},
							"functionCall": {"name": "safe"}
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	outputStr := string(output)

	for i := 0; i < 8; i++ {
		sig := gjson.Get(outputStr, fmt.Sprintf("request.contents.0.parts.%d.thoughtSignature", i))
		if sig.Exists() {
			t.Fatalf("part %d: expected all duplicate thoughtSignature fields to be stripped, got %s", i, sig.Raw)
		}
		fcSig := gjson.Get(outputStr, fmt.Sprintf("request.contents.0.parts.%d.functionCall.thoughtSignature", i))
		if fcSig.Exists() {
			t.Fatalf("part %d: expected functionCall thoughtSignature to be stripped, got %s", i, fcSig.Raw)
		}
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_StringValueNotTreatedAsKey(t *testing.T) {
	// A part where "thoughtSignature" is a tool name (string value), not a key, should not trigger signature sanitization
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"functionCall": {
								"name": "thoughtSignature",
								"args": {
									"query": "thought_signature"
								}
							}
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	// Output should preserve the exact input string because no signature keys exist
	if string(output) != string(inputJSON) {
		t.Fatalf("expected unchanged output for non-key values, got %s", string(output))
	}
}

func TestSanitizeAntigravityClaudeGeminiRequestSignatures_LargeNumberDoesNotHaltKeyScan(t *testing.T) {
	// A part with numbers outside float64 range should not break token scanning
	inputJSON := []byte(`{
		"project": "",
		"model": "claude-sonnet-4-6",
		"request": {
			"contents": [
				{
					"role": "model",
					"parts": [
						{
							"functionCall": {"args": {"n": 1e10000}},
							"functionCall": {"thoughtSignature": "secret"},
							"functionCall": {"name": "safe"}
						}
					]
				}
			]
		}
	}`)

	output := SanitizeAntigravityClaudeGeminiRequestSignatures("claude-sonnet-4-6", inputJSON)
	outputStr := string(output)

	fcSig := gjson.Get(outputStr, "request.contents.0.parts.0.functionCall.thoughtSignature")
	if fcSig.Exists() {
		t.Fatalf("expected hidden thoughtSignature to be stripped despite large number, got %s", fcSig.Raw)
	}
}

func TestFixCLIToolResponse_AttachesSiblingInlineDataToNearestFunctionResponse(t *testing.T) {
	tests := []struct {
		name  string
		parts string
		want  []struct {
			id   string
			mime string
			data string
		}
	}{
		{
			name: "snake_case sibling after single response",
			parts: `{"functionResponse":{"name":"read","response":{"result":"Read image file [image/png]"},"id":"call_1"}},` +
				`{"inline_data":{"mime_type":"image/png","data":"QUJD"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{{id: "call_1", mime: "image/png", data: "QUJD"}},
		},
		{
			name: "camelCase sibling after single response",
			parts: `{"functionResponse":{"name":"read","response":{"result":"ok"},"id":"call_1"}},` +
				`{"inlineData":{"mimeType":"image/webp","data":"NEW"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{{id: "call_1", mime: "image/webp", data: "NEW"}},
		},
		{
			name: "append sibling onto existing functionResponse.parts",
			parts: `{"functionResponse":{"name":"read","response":{"result":"ok"},"id":"call_1","parts":[{"inlineData":{"mimeType":"image/gif","data":"OLD"}}]}},` +
				`{"inlineData":{"mimeType":"image/webp","data":"NEW"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{
				{id: "call_1", mime: "image/gif", data: "OLD"},
			},
		},
		{
			name: "interleaved siblings attach to nearest response",
			parts: `{"functionResponse":{"name":"read","response":{"result":"A"},"id":"call_a"}},` +
				`{"inline_data":{"mime_type":"image/png","data":"AAA"}},` +
				`{"functionResponse":{"name":"read","response":{"result":"B"},"id":"call_b"}},` +
				`{"inline_data":{"mime_type":"image/jpeg","data":"BBB"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{
				{id: "call_a", mime: "image/png", data: "AAA"},
				{id: "call_b", mime: "image/jpeg", data: "BBB"},
			},
		},
		{
			name: "leading sibling attaches to first response",
			parts: `{"inline_data":{"mime_type":"image/png","data":"LEAD"}},` +
				`{"functionResponse":{"name":"read","response":{"result":"A"},"id":"call_a"}},` +
				`{"functionResponse":{"name":"read","response":{"result":"B"},"id":"call_b"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{
				{id: "call_a", mime: "image/png", data: "LEAD"},
			},
		},
		{
			name: "missing mimeType defaults to image/png",
			parts: `{"functionResponse":{"name":"read","response":{"result":"ok"},"id":"call_1"}},` +
				`{"inlineData":{"data":"QUJD"}}`,
			want: []struct {
				id   string
				mime string
				data string
			}{{id: "call_1", mime: "image/png", data: "QUJD"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modelParts := `{"functionCall":{"name":"read","id":"call_1"}}`
			if tt.name == "interleaved siblings attach to nearest response" || tt.name == "leading sibling attaches to first response" {
				modelParts = `{"functionCall":{"name":"read","id":"call_a"}},{"functionCall":{"name":"read","id":"call_b"}}`
			}
			input := `{"request":{"contents":[` +
				`{"role":"model","parts":[` + modelParts + `]},` +
				`{"role":"user","parts":[` + tt.parts + `]}` +
				`]}}`
			result, err := fixCLIToolResponse([]byte(input))
			if err != nil {
				t.Fatalf("fixCLIToolResponse failed: %v", err)
			}
			contents := gjson.GetBytes(result, "request.contents").Array()
			if len(contents) != 2 {
				t.Fatalf("contents = %d, want 2. Output: %s", len(contents), result)
			}
			funcParts := contents[1].Get("parts").Array()
			gotByID := map[string][]gjson.Result{}
			for _, part := range funcParts {
				fr := part.Get("functionResponse")
				gotByID[fr.Get("id").String()] = fr.Get("parts").Array()
			}
			for _, want := range tt.want {
				images := gotByID[want.id]
				found := false
				for _, img := range images {
					if img.Get("inlineData.data").String() == want.data && img.Get("inlineData.mimeType").String() == want.mime {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("id=%s missing inlineData mime=%s data=%s. Output: %s", want.id, want.mime, want.data, result)
				}
			}
			if tt.name == "interleaved siblings attach to nearest response" {
				if len(gotByID["call_a"]) != 1 || len(gotByID["call_b"]) != 1 {
					t.Fatalf("nearest attribution failed: A=%d B=%d. Output: %s", len(gotByID["call_a"]), len(gotByID["call_b"]), result)
				}
			}
			if tt.name == "leading sibling attaches to first response" {
				if len(gotByID["call_b"]) != 0 {
					t.Fatalf("leading image leaked onto call_b. Output: %s", result)
				}
			}
			if tt.name == "append sibling onto existing functionResponse.parts" {
				images := gotByID["call_1"]
				if len(images) != 2 {
					t.Fatalf("existing+sibling parts = %d, want 2. Output: %s", len(images), result)
				}
				if images[1].Get("inlineData.data").String() != "NEW" {
					t.Fatalf("appended sibling data = %q, want NEW. Output: %s", images[1].Get("inlineData.data").String(), result)
				}
			}
		})
	}
}

func TestConvertGeminiRequestToAntigravity_PreservesSiblingToolImageOnUserRole(t *testing.T) {
	input := []byte(`{
		"contents": [
			{"role":"user","parts":[{"text":"read file"}]},
			{"role":"model","parts":[{"functionCall":{"name":"read","args":{},"id":"call_1"}}]},
			{"role":"user","parts":[
				{"functionResponse":{"name":"read","response":{"result":"Read image file [image/png]"},"id":"call_1"}},
				{"inline_data":{"mime_type":"image/png","data":"QUJD"}}
			]}
		]
	}`)
	out := ConvertGeminiRequestToAntigravity("gemini-3-flash", input, false)
	contents := gjson.GetBytes(out, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents = %d, want 3. Output: %s", len(contents), out)
	}
	funcContent := contents[2]
	if got := funcContent.Get("role").String(); got != "user" {
		t.Fatalf("role = %q, want user after Antigravity normalization. Output: %s", got, out)
	}
	funcResp := funcContent.Get("parts.0.functionResponse")
	if !funcResp.Exists() {
		t.Fatalf("functionResponse missing. Output: %s", out)
	}
	if got := funcResp.Get("id").String(); got != "call_1" {
		t.Fatalf("id = %q, want call_1", got)
	}
	if got := funcResp.Get("response.result").String(); got != "Read image file [image/png]" {
		t.Fatalf("result = %q", got)
	}
	inlineData := funcResp.Get("parts.0.inlineData")
	if !inlineData.Exists() {
		t.Fatalf("functionResponse.parts.0.inlineData missing. Output: %s", out)
	}
	if got := inlineData.Get("mimeType").String(); got != "image/png" {
		t.Fatalf("mimeType = %q, want image/png", got)
	}
	if got := inlineData.Get("data").String(); got != "QUJD" {
		t.Fatalf("data = %q, want QUJD", got)
	}
	if funcContent.Get("parts.1.inline_data").Exists() || funcContent.Get("parts.1.inlineData").Exists() {
		t.Fatalf("sibling inline data should be absorbed into functionResponse.parts. Output: %s", out)
	}
}
