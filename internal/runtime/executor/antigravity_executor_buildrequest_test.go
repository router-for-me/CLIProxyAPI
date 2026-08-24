package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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

func TestAntigravityBuildRequest_SkipsSchemaSanitizationWithoutToolsField(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-image", []byte(`{
		"request": {
			"contents": [
				{
					"role": "user",
					"x-debug": "keep-me",
					"parts": [
						{
							"text": "hello"
						}
					]
				}
			],
			"nonSchema": {
				"nullable": true,
				"x-extra": "keep-me"
			},
			"generationConfig": {
				"maxOutputTokens": 128
			}
		}
	}`))

	assertNonSchemaRequestPreserved(t, body)
}

func TestAntigravityBuildRequest_SkipsSchemaSanitizationWithEmptyToolsArray(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-image", []byte(`{
		"request": {
			"tools": [],
			"contents": [
				{
					"role": "user",
					"x-debug": "keep-me",
					"parts": [
						{
							"text": "hello"
						}
					]
				}
			],
			"nonSchema": {
				"nullable": true,
				"x-extra": "keep-me"
			},
			"generationConfig": {
				"maxOutputTokens": 128
			}
		}
	}`))

	assertNonSchemaRequestPreserved(t, body)
}

func TestAntigravityBuildRequest_UsesAuthProjectID(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-pro", []byte(`{
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "hello"}]
				}
			]
		}
	}`))

	if got, ok := body["project"].(string); !ok || got != "project-1" {
		t.Fatalf("project should come from auth metadata, got=%v", body["project"])
	}
}

func TestAntigravityBuildRequest_UsesRouteModelWhenPayloadContainsDifferentModel(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3-flash-agent", []byte(`{
		"model": "gemini-3.1-flash-lite",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Perform a web search"}]
				}
			],
			"tools": [{"googleSearch": {}}]
		}
	}`))

	if got, ok := body["model"].(string); !ok || got != "gemini-3-flash-agent" {
		t.Fatalf("request model should stay on route model, got=%v", body["model"])
	}
}

func TestAntigravityBuildRequestUsesDerivedSessionIDAndPreservesExplicit(t *testing.T) {
	t.Parallel()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	req, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", payload, false, "", "https://example.com", "-123456789")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}
	body := requestBody(t, req)
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid: %v", body["request"])
	}
	if got := request["sessionId"]; got != "-123456789" {
		t.Fatalf("request.sessionId = %v, want -123456789", got)
	}

	explicitPayload := []byte(`{"request":{"sessionId":"-987654321","contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	explicitReq, errExplicit := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", explicitPayload, false, "", "https://example.com", "-123456789")
	if errExplicit != nil {
		t.Fatalf("buildRequest explicit error: %v", errExplicit)
	}
	explicitBody := requestBody(t, explicitReq)
	explicitRequest, ok := explicitBody["request"].(map[string]any)
	if !ok {
		t.Fatalf("explicit request missing or invalid: %v", explicitBody["request"])
	}
	if got := explicitRequest["sessionId"]; got != "-987654321" {
		t.Fatalf("explicit request.sessionId = %v, want -987654321", got)
	}
}

func TestAntigravityBuildRequest_PreservesIndependentWebSearchRequestType(t *testing.T) {
	body := buildRequestBodyFromRawPayload(t, "gemini-3.1-flash-lite", []byte(`{
		"requestType": "web_search",
		"request": {
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "北京天气 2026-06-12"}]
				}
			],
			"tools": [
				{
					"googleSearch": {
						"enhancedContent": {
							"imageSearch": {
								"maxResultCount": 5
							}
						}
					}
				}
			],
			"generationConfig": {
				"candidateCount": 1
			}
		}
	}`))

	if got, ok := body["requestType"].(string); !ok || got != "web_search" {
		t.Fatalf("requestType should stay web_search, got=%v", body["requestType"])
	}
	if _, ok := body["requestId"]; ok {
		t.Fatalf("web_search request should not add requestId: %v", body["requestId"])
	}
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid: %v", body["request"])
	}
	if _, ok := request["sessionId"]; ok {
		t.Fatalf("web_search request should not add request.sessionId: %v", request["sessionId"])
	}
	if got, ok := body["project"].(string); !ok || got != "project-1" {
		t.Fatalf("project should come from auth metadata, got=%v", body["project"])
	}
}

func TestShouldResolveAntigravityWebSearchGroundingURLsRequiresTypedWebSearchAndSearchRequest(t *testing.T) {
	original := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`)
	translatedWithGoogleSearch := []byte(`{"requestType":"web_search","request":{"tools":[{"googleSearch":{}}]}}`)
	translatedWithoutGoogleSearch := []byte(`{"request":{"contents":[]}}`)

	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithGoogleSearch) {
		t.Fatal("expected typed Claude web search translated to web_search request to resolve grounding URLs")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithoutGoogleSearch) {
		t.Fatal("expected request without googleSearch to skip grounding URL resolution")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAI, original, translatedWithGoogleSearch) {
		t.Fatal("expected non-Claude source format to skip grounding URL resolution")
	}
}

func TestAntigravityPrepareRequestAuth_FetchesMissingProjectID(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("unexpected project discovery request: %s", req.URL.String())
		}
		if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
			t.Fatalf("X-Goog-Api-Client = %q, want empty", got)
		}
		raw, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			t.Fatalf("read discovery body: %v", errRead)
		}
		if !strings.Contains(string(raw), `"ideType":"ANTIGRAVITY"`) {
			t.Fatalf("unexpected discovery body: %s", string(raw))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"fetched-project"}`)),
		}, nil
	}))

	updated, err := executor.PrepareRequestAuth(ctx, auth)
	if err != nil {
		t.Fatalf("PrepareRequestAuth error: %v", err)
	}
	if updated == nil {
		t.Fatalf("PrepareRequestAuth returned nil auth")
	}
	if _, ok := auth.Metadata["project_id"]; ok {
		t.Fatalf("original auth metadata should not be mutated")
	}
	if got, ok := updated.Metadata["project_id"].(string); !ok || got != "fetched-project" {
		t.Fatalf("updated auth metadata project_id = %v, want fetched-project", updated.Metadata["project_id"])
	}
}

func TestAntigravityBuildRequest_RejectsMissingProjectID(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{}}

	_, err := executor.buildRequest(context.Background(), auth, "token", "gemini-3.1-pro", []byte(`{"request":{}}`), false, "", "https://example.com")
	if err == nil {
		t.Fatalf("buildRequest should fail when auth has no project_id")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error should expose status code, got %T", err)
	}
	if got := status.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadRequest)
	}
}

func TestAntigravityBuildRequest_CapsMaxOutputTokensWithSchemaSanitization(t *testing.T) {
	clientID := "test-client-antigravity-cap"
	modelName := "gemini-test-cap-64-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{
			ID:                  modelName,
			MaxCompletionTokens: 64,
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	body := buildRequestBodyFromRawPayload(t, modelName, []byte(`{
		"request": {
			"generationConfig": {
				"maxOutputTokens": 128
			},
			"tools": [
				{
					"function_declarations": [
						{
							"name": "test_tool",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"query": {"type": "string"}
								}
							}
						}
					]
				}
			]
		}
	}`))

	generationConfig := extractGenerationConfig(t, body)
	if got, ok := generationConfig["maxOutputTokens"].(float64); !ok || got != 64 {
		t.Fatalf("maxOutputTokens = %v, want 64", generationConfig["maxOutputTokens"])
	}
}

func TestAntigravityBuildRequest_CapsMaxOutputTokensWithoutSchemaSanitization(t *testing.T) {
	clientID := "test-client-antigravity-cap-table"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{ID: "cap-max-completion-100", MaxCompletionTokens: 100},
		{ID: "cap-output-limit-200", OutputTokenLimit: 200},
		{ID: "cap-both-300-500", OutputTokenLimit: 300, MaxCompletionTokens: 500},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	tests := []struct {
		name      string
		modelName string
		value     string
		want      float64
	}{
		{name: "above max completion tokens", modelName: "cap-max-completion-100", value: "500", want: 100},
		{name: "below limit", modelName: "cap-max-completion-100", value: "50", want: 50},
		{name: "at limit", modelName: "cap-max-completion-100", value: "100", want: 100},
		{name: "very large integer", modelName: "cap-max-completion-100", value: "18446744073709551615", want: 100},
		{name: "output token limit only", modelName: "cap-output-limit-200", value: "500", want: 200},
		{name: "output token limit preferred", modelName: "cap-both-300-500", value: "400", want: 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"request":{"generationConfig":{"maxOutputTokens":` + tt.value + `}}}`)
			body := buildRequestBodyFromRawPayload(t, tt.modelName, payload)
			generationConfig := extractGenerationConfig(t, body)
			if got, ok := generationConfig["maxOutputTokens"].(float64); !ok || got != tt.want {
				t.Errorf("maxOutputTokens = %v, want %v", generationConfig["maxOutputTokens"], tt.want)
			}
		})
	}
}

func TestAntigravityBuildRequest_CapsClaudeThinkingBudget(t *testing.T) {
	clientID := "test-client-antigravity-claude-cap"
	modelName := "claude-test-cap-64-model"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{
			ID:                  modelName,
			MaxCompletionTokens: 64,
			Thinking: &registry.ThinkingSupport{
				Min: 8,
				Max: 64,
			},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	tests := []struct {
		name         string
		maxOut       int
		budget       int
		wantMax      float64
		wantBudget   float64
		wantNoConfig bool
	}{
		{name: "caps budget below capped output", maxOut: 128, budget: 64, wantMax: 64, wantBudget: 63},
		{name: "preserves valid lower budget", maxOut: 128, budget: 32, wantMax: 64, wantBudget: 32},
		{name: "removes config below minimum", maxOut: 8, budget: 8, wantMax: 8, wantNoConfig: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := fmt.Sprintf(`{"request":{"generationConfig":{"maxOutputTokens":%d,"thinkingConfig":{"thinkingBudget":%d}}}}`, tt.maxOut, tt.budget)
			body := buildRequestBodyFromRawPayload(t, modelName, []byte(payload))
			generationConfig := extractGenerationConfig(t, body)
			if got, ok := generationConfig["maxOutputTokens"].(float64); !ok || got != tt.wantMax {
				t.Errorf("maxOutputTokens = %v, want %v", generationConfig["maxOutputTokens"], tt.wantMax)
			}
			tc, ok := generationConfig["thinkingConfig"].(map[string]any)
			if tt.wantNoConfig {
				if ok && tc != nil {
					t.Errorf("thinkingConfig should be removed, got %v", tc)
				}
			} else {
				if !ok || tc == nil {
					t.Fatalf("thinkingConfig missing or invalid type")
				}
				if got, ok := tc["thinkingBudget"].(float64); !ok || got != tt.wantBudget {
					t.Errorf("thinkingBudget = %v, want %v", tc["thinkingBudget"], tt.wantBudget)
				}
			}
		})
	}
}

func extractGenerationConfig(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}
	generationConfig, ok := request["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing or invalid type")
	}
	return generationConfig
}

func assertNonSchemaRequestPreserved(t *testing.T, body map[string]any) {
	t.Helper()

	request, ok := body["request"].(map[string]any)
	if !ok {
		t.Fatalf("request missing or invalid type")
	}

	contents, ok := request["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing or empty")
	}
	content, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("content missing or invalid type")
	}
	if got, ok := content["x-debug"].(string); !ok || got != "keep-me" {
		t.Fatalf("x-debug should be preserved when no tool schema exists, got=%v", content["x-debug"])
	}

	nonSchema, ok := request["nonSchema"].(map[string]any)
	if !ok {
		t.Fatalf("nonSchema missing or invalid type")
	}
	if _, ok := nonSchema["nullable"]; !ok {
		t.Fatalf("nullable should be preserved outside schema cleanup path")
	}
	if got, ok := nonSchema["x-extra"].(string); !ok || got != "keep-me" {
		t.Fatalf("x-extra should be preserved outside schema cleanup path, got=%v", nonSchema["x-extra"])
	}

	generationConfig, ok := request["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig missing or invalid type")
	}
	if got, ok := generationConfig["maxOutputTokens"].(float64); !ok || got != 128 {
		t.Fatalf("maxOutputTokens = %v, want 128", generationConfig["maxOutputTokens"])
	}
}

func buildRequestBodyFromPayload(t *testing.T, modelName string) map[string]any {
	t.Helper()
	return buildRequestBodyFromRawPayload(t, modelName, []byte(`{
		"request": {
			"tools": [
				{
					"function_declarations": [
						{
							"name": "tool_1",
							"parametersJsonSchema": {
								"$schema": "http://json-schema.org/draft-07/schema#",
								"$id": "root-schema",
								"$comment": "root comment should be removed",
								"type": "object",
								"properties": {
									"$id": {"type": "string"},
									"arg": {
										"type": "object",
										"$comment": "nested comment should be removed",
										"prefill": "hello",
										"properties": {
											"mode": {
												"type": "string",
												"deprecated": true,
												"enum": ["a", "b"],
												"enumDescriptions": ["Alpha", "Beta"],
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
	}`))
}

func buildRequestBodyFromRawPayload(t *testing.T, modelName string, payload []byte) map[string]any {
	t.Helper()

	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}

	req, err := executor.buildRequest(context.Background(), auth, "token", modelName, payload, false, "", "https://example.com")
	if err != nil {
		t.Fatalf("buildRequest error: %v", err)
	}

	return requestBody(t, req)
}

func requestBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()

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
	if _, ok := params["$comment"]; ok {
		t.Fatalf("root $comment should be removed from schema")
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
	if _, ok := arg["$comment"]; ok {
		t.Fatalf("nested $comment should be removed from schema")
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
	if _, ok := mode["enumDescriptions"]; ok {
		t.Fatalf("enumDescriptions should be removed from nested schema")
	}
	if _, ok := mode["deprecated"]; ok {
		t.Fatalf("deprecated should be removed from nested schema")
	}
}
