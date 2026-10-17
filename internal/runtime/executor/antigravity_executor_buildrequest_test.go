package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestResolveAntigravityRequestBaseURL(t *testing.T) {
	t.Run("default uses daily endpoint", func(t *testing.T) {
		if got := resolveAntigravityRequestBaseURL(&cliproxyauth.Auth{}); got != antigravityBaseURLDaily {
			t.Fatalf("base URL = %q, want %q", got, antigravityBaseURLDaily)
		}
	})

	t.Run("custom attribute endpoint remains supported", func(t *testing.T) {
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": "https://enterprise.example.com/"}}
		if got := resolveAntigravityRequestBaseURL(auth); got != "https://enterprise.example.com" {
			t.Fatalf("base URL = %q, want custom endpoint", got)
		}
	})

	t.Run("custom auth file endpoint remains supported", func(t *testing.T) {
		auth := &cliproxyauth.Auth{Metadata: map[string]any{"base_url": "https://enterprise.example.com/"}}
		if got := resolveAntigravityRequestBaseURL(auth); got != "https://enterprise.example.com" {
			t.Fatalf("base URL = %q, want custom auth file endpoint", got)
		}
	})
}

func TestAntigravityLoadCodeAssistBaseURLRemainsProdByDefault(t *testing.T) {
	if got := antigravityLoadCodeAssistBaseURL(&cliproxyauth.Auth{}); got != antigravityBaseURLProd {
		t.Fatalf("loadCodeAssist base URL = %q, want %q", got, antigravityBaseURLProd)
	}
}

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
	originalResponses := []byte(`{"tools":[{"type":"web_search"}]}`)
	originalResponsesPreview := []byte(`{"tools":[{"type":"web_search_preview_2025_03_11"}]}`)
	translatedWithGoogleSearch := []byte(`{"requestType":"web_search","request":{"tools":[{"googleSearch":{}}]}}`)
	translatedWithoutGoogleSearch := []byte(`{"request":{"contents":[]}}`)

	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithGoogleSearch) {
		t.Fatal("expected typed Claude web search translated to web_search request to resolve grounding URLs")
	}
	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAIResponse, originalResponses, translatedWithGoogleSearch) {
		t.Fatal("expected typed OpenAI Responses web search translated to web_search request to resolve grounding URLs")
	}
	if !shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAIResponse, originalResponsesPreview, translatedWithGoogleSearch) {
		t.Fatal("expected web_search_preview_2025_03_11 translated to web_search request to resolve grounding URLs")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatClaude, original, translatedWithoutGoogleSearch) {
		t.Fatal("expected request without googleSearch to skip grounding URL resolution")
	}
	if shouldResolveAntigravityWebSearchGroundingURLs(sdktranslator.FormatOpenAI, original, translatedWithGoogleSearch) {
		t.Fatal("expected non-Claude/non-Responses source format to skip grounding URL resolution")
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

func TestAntigravityPrepareRequestAuth_UpstreamForbiddenPreserves403(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":403,"message":"The caller does not have permission"}}`)),
		}, nil
	}))

	_, err := executor.PrepareRequestAuth(ctx, auth)
	if err == nil {
		t.Fatalf("PrepareRequestAuth should fail on upstream 403")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error should expose StatusCode(), got %T (%v)", err, err)
	}
	if got := status.StatusCode(); got != http.StatusForbidden {
		t.Fatalf("status code = %d, want %d", got, http.StatusForbidden)
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

	if generationConfig, ok := request["generationConfig"].(map[string]any); ok {
		if _, ok := generationConfig["maxOutputTokens"]; ok {
			t.Fatalf("maxOutputTokens should still be removed for non-Claude requests")
		}
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

func TestAntigravityBuildRequest_Gemini4ArgonTierRouting(t *testing.T) {
	const contents = `"contents":[{"role":"user","parts":[{"text":"hi"}]}]`
	withThinking := func(cfg string) []byte {
		return []byte(`{"request":{"generationConfig":{"thinkingConfig":` + cfg + `},` + contents + `}}`)
	}
	tests := []struct {
		name        string
		model       string
		payload     []byte
		wantModel   string
		wantInclude any // nil: absent
		wantNoThink bool
	}{
		{name: "bare alias defaults to high", model: "gemini-4-argon", payload: []byte(`{"request":{` + contents + `}}`), wantModel: "gemini-4-argon-high"},
		{name: "high suffix", model: "gemini-4-argon-high", payload: []byte(`{"request":{` + contents + `}}`), wantModel: "gemini-4-argon-high"},
		{name: "medium suffix", model: "gemini-4-argon-medium", payload: []byte(`{"request":{` + contents + `}}`), wantModel: "gemini-4-argon-medium"},
		{name: "low suffix", model: "gemini-4-argon-low", payload: []byte(`{"request":{` + contents + `}}`), wantModel: "gemini-4-argon-low"},
		{name: "level overrides suffix upward", model: "gemini-4-argon-low", payload: withThinking(`{"thinkingLevel":"high"}`), wantModel: "gemini-4-argon-high"},
		{name: "level overrides suffix downward and keeps includeThoughts", model: "gemini-4-argon-high", payload: withThinking(`{"thinkingLevel":"low","includeThoughts":true}`), wantModel: "gemini-4-argon-low", wantInclude: true},
		{name: "explicit includeThoughts false is kept", model: "gemini-4-argon-high", payload: withThinking(`{"thinkingLevel":"medium","includeThoughts":false}`), wantModel: "gemini-4-argon-medium", wantInclude: false},
		{name: "numeric budget selects tier", model: "gemini-4-argon", payload: withThinking(`{"thinkingBudget":8192}`), wantModel: "gemini-4-argon-medium"},
		{name: "level none clamps to the low tier", model: "gemini-4-argon-high", payload: withThinking(`{"thinkingLevel":"none"}`), wantModel: "gemini-4-argon-low"},
		{name: "budget zero keeps the suffix tier", model: "gemini-4-argon-medium", payload: withThinking(`{"thinkingBudget":0}`), wantModel: "gemini-4-argon-medium"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := buildRequestBodyFromRawPayload(t, tc.model, tc.payload)
			assertArgonTierRequest(t, body, tc.wantModel, tc.wantInclude)
		})
	}
}

func assertArgonTierRequest(t *testing.T, body map[string]any, wantModel string, wantInclude any) {
	t.Helper()
	if got := body["model"]; got != wantModel {
		t.Fatalf("model = %v, want %s", got, wantModel)
	}
	genCfg, _ := body["request"].(map[string]any)["generationConfig"].(map[string]any)
	thinkCfg, hasThink := genCfg["thinkingConfig"].(map[string]any)
	if !hasThink {
		t.Fatalf("thinkingConfig missing")
	}
	if wantModel == "gemini-4-argon-high" {
		if got := thinkCfg["thinkingLevel"]; got != "high" {
			t.Fatalf("thinkingLevel = %v, want high", got)
		}
		if _, ok := thinkCfg["thinkingBudget"]; ok {
			t.Fatalf("thinkingBudget must not be sent to gemini-4-argon-high: %v", thinkCfg)
		}
	} else {
		if got := thinkCfg["thinkingBudget"]; got != float64(-1) {
			t.Fatalf("thinkingBudget = %v, want -1", got)
		}
		if _, ok := thinkCfg["thinkingLevel"]; ok {
			t.Fatalf("thinkingLevel must not be sent to %s: %v", wantModel, thinkCfg)
		}
	}
	if got, ok := thinkCfg["includeThoughts"]; wantInclude == nil && ok {
		t.Fatalf("includeThoughts = %v, want absent", got)
	} else if wantInclude != nil && got != wantInclude {
		t.Fatalf("includeThoughts = %v, want %v", got, wantInclude)
	}
}

// TestAntigravityBuildRequest_Gemini4ArgonThinkingPipeline runs the real
// normalization step (thinking validation + Antigravity applier) ahead of
// buildRequest, so the tier resolver is exercised with the payload shape it
// receives in the executor rather than with hand-written thinkingConfig.
func TestAntigravityBuildRequest_Gemini4ArgonThinkingPipeline(t *testing.T) {
	const contents = `"contents":[{"role":"user","parts":[{"text":"hi"}]}]`
	tests := []struct {
		name      string
		model     string
		thinking  string
		wantModel string
	}{
		{name: "explicit none clamps to low tier", model: "gemini-4-argon-high", thinking: `{"thinkingLevel":"none"}`, wantModel: "gemini-4-argon-low"},
		{name: "budget zero clamps to low tier", model: "gemini-4-argon-medium", thinking: `{"thinkingBudget":0}`, wantModel: "gemini-4-argon-low"},
		{name: "budget selects medium tier", model: "gemini-4-argon-high", thinking: `{"thinkingBudget":8192}`, wantModel: "gemini-4-argon-medium"},
		{name: "high level keeps high tier", model: "gemini-4-argon-low", thinking: `{"thinkingLevel":"high"}`, wantModel: "gemini-4-argon-high"},
		{name: "dynamic budget keeps suffix tier", model: "gemini-4-argon-medium", thinking: `{"thinkingBudget":-1}`, wantModel: "gemini-4-argon-medium"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelInfo := registry.LookupModelInfo(tc.model, "antigravity")
			if modelInfo == nil {
				t.Fatalf("model %s missing from antigravity registry", tc.model)
			}
			payload := []byte(`{"request":{"generationConfig":{"thinkingConfig":` + tc.thinking + `},` + contents + `}}`)
			normalized, err := thinking.ApplyThinkingWithModelInfo(payload, payload, tc.model, "antigravity", "antigravity", "antigravity", modelInfo)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo error: %v", err)
			}
			body := buildRequestBodyFromRawPayload(t, tc.model, normalized)
			assertArgonTierRequest(t, body, tc.wantModel, nil)
		})
	}
}

func TestAntigravityBuildRequest_SanitizesToolSchemaForArgonAndFlash38(t *testing.T) {
	for _, m := range []string{"gemini-4-argon-high", "gemini-3.8-flash-high"} {
		body := buildRequestBodyFromPayload(t, m)
		decl := extractFirstFunctionDeclaration(t, body)
		params, ok := decl["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("%s: parameters missing or invalid type", m)
		}
		assertSchemaSanitizedAndPropertyPreserved(t, params)
	}
}
