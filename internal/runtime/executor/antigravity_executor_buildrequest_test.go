package executor

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestAntigravityBuildRequest_SanitizesGeminiToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "gemini-2.5-pro")

	decl := extractFirstFunctionDeclaration(t, body)
	if _, ok := decl["parametersJsonSchema"]; ok {
		t.Fatalf("parametersJsonSchema should be renamed to parameters")
	}

	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestAntigravityBuildRequest_SanitizesAntigravityToolSchema(t *testing.T) {
	body := buildRequestBodyFromPayload(t, "claude-opus-4-6")

	decl := extractFirstFunctionDeclaration(t, body)
	params, ok := decl["parameters"].(map[string]any)
	if !ok {
		t.Fatalf("parameters missing or invalid type")
	}
	assertSchemaSanitizedAndPropertyPreserved(t, params)
}

func TestSanitizeAntigravityClaudeCompatFields_RemovesOutputConfig(t *testing.T) {
	input := []byte(`{
		"output_config":{"effort":"max"},
		"request":{
			"output_config":{"effort":"high"},
			"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}
		}
	}`)

	out := sanitizeAntigravityClaudeCompatFields(input)

	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal sanitized body error: %v, body=%s", err, string(out))
	}
	if _, ok := body["output_config"]; ok {
		t.Fatalf("top-level output_config should be removed")
	}
	req, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	if _, ok := req["output_config"]; ok {
		t.Fatalf("request.output_config should be removed")
	}
	if _, ok := req["generationConfig"]; !ok {
		t.Fatalf("request.generationConfig should be preserved")
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_ClampsThinkingModel(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6-thinking", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_PreservesWithinLimit(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":64000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6-thinking", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 64000 {
		t.Fatalf("expected maxOutputTokens=64000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_PreservesNonThinkingModel(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("claude-opus-4-6", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 128000 {
		t.Fatalf("expected maxOutputTokens=128000, got %d", got)
	}
}

func TestClampAntigravityClaudeMaxOutputTokens_NoopForNonClaude(t *testing.T) {
	input := []byte(`{"request":{"generationConfig":{"maxOutputTokens":128000}}}`)
	out := clampAntigravityClaudeMaxOutputTokens("gemini-2.5-pro", input)
	if got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int(); got != 128000 {
		t.Fatalf("expected maxOutputTokens=128000, got %d", got)
	}
}

func buildRequestBodyFromPayload(t *testing.T, modelName string) map[string]any {
	t.Helper()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{}
	payload := []byte(`{
		"request": {
			"tools": [
				{
					"function_declarations": [
						{
							"name": "tool_1",
							"parametersJsonSchema": {
								"$schema": "http://json-schema.org/draft-07/schema#",
								"$id": "root-schema",
								"type": "object",
								"properties": {
									"$id": {"type": "string"},
									"arg": {
										"type": "object",
										"prefill": "hello",
										"properties": {
											"mode": {
												"type": "string",
												"enum": ["a", "b"],
												"enumTitles": ["A", "B"]
											}
										}
									}
								},
								"patternProperties": {
									"^x-": {"type": "string"}
								}
							}
						}
					]
				}
			]
		}
	}`)

	req, err := executor.buildRequest(context.Background(), auth, "token", modelName, payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal request body error: %v, body=%s", err, string(raw))
	}
	return body
}

func extractFirstFunctionDeclaration(t *testing.T, body map[string]any) map[string]any {
	t.Helper()

	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools missing or empty")
	}
	tool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("first tool invalid type")
	}
	decls, ok := tool["function_declarations"].([]any)
	if !ok || len(decls) == 0 {
		t.Fatalf("function_declarations missing or empty")
	}
	decl, ok := decls[0].(map[string]any)
	if !ok {
		t.Fatalf("first function declaration invalid type")
	}
	return decl
}

func assertSchemaSanitizedAndPropertyPreserved(t *testing.T, params map[string]any) {
	t.Helper()

	if _, ok := params["$id"]; ok {
		t.Fatalf("root $id should be removed from schema")
	}
	if _, ok := params["patternProperties"]; ok {
		t.Fatalf("patternProperties should be removed from schema")
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or invalid type")
	}
	if _, ok := props["$id"]; !ok {
		t.Fatalf("property named $id should be preserved")
	}

	arg, ok := props["arg"].(map[string]any)
	if !ok {
		t.Fatalf("arg property missing or invalid type")
	}
	if _, ok := arg["prefill"]; ok {
		t.Fatalf("prefill should be removed from nested schema")
	}

	argProps, ok := arg["properties"].(map[string]any)
	if !ok {
		t.Fatalf("arg.properties missing or invalid type")
	}
	mode, ok := argProps["mode"].(map[string]any)
	if !ok {
		t.Fatalf("mode property missing or invalid type")
	}
	if _, ok := mode["enumTitles"]; ok {
		t.Fatalf("enumTitles should be removed from nested schema")
	}
}
