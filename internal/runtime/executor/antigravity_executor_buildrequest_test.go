package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

func TestAntigravityLoadCodeAssistBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{"default daily", &cliproxyauth.Auth{}, antigravityBaseURLDaily},
		{"custom attribute", &cliproxyauth.Auth{Attributes: map[string]string{"base_url": antigravityBaseURLProd + "/"}}, antigravityBaseURLProd},
		{"custom metadata", &cliproxyauth.Auth{Metadata: map[string]any{"base_url": "https://enterprise.example.com/"}}, "https://enterprise.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := antigravityLoadCodeAssistBaseURL(tc.auth); got != tc.want {
				t.Fatalf("loadCodeAssist base URL = %q, want %q", got, tc.want)
			}
		})
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
		if req.URL.String() != "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
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

func TestAntigravityExecutor_Claude55ExplicitThinkingLevelPreserved(t *testing.T) {
	sseResponse := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n"
	for _, model := range []string{"claude-sonnet-5-5-high", "claude-opus-5-5-high"} {
		for _, level := range []string{"low", "medium", "high"} {
			for _, stream := range []bool{false, true} {
				modeName := "non_stream"
				if stream {
					modeName = "stream"
				}
				t.Run(model+"/"+level+"/"+modeName, func(t *testing.T) {
					var capturedBody []byte
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						raw, errRead := io.ReadAll(req.Body)
						if errRead != nil {
							return nil, errRead
						}
						if errClose := req.Body.Close(); errClose != nil {
							return nil, errClose
						}
						capturedBody = raw
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"text/event-stream"}},
							Body:       io.NopCloser(strings.NewReader(sseResponse)),
							Request:    req,
						}, nil
					}))

					executor := NewAntigravityExecutor(&config.Config{})
					auth := &cliproxyauth.Auth{
						ID:       "ag-claude-55-test",
						Provider: "antigravity",
						Metadata: map[string]any{
							"access_token": "token",
							"project_id":   "project-1",
							"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
						},
					}
					payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"Reply OK"}]}],"generationConfig":{"maxOutputTokens":128000,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"` + level + `"}}}`)
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{
						SourceFormat:    sdktranslator.FormatGemini,
						ResponseFormat:  sdktranslator.FormatGemini,
						OriginalRequest: payload,
						Stream:          stream,
					}

					if stream {
						res, errStream := executor.ExecuteStream(ctx, auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
						}
					} else {
						if _, errExec := executor.Execute(ctx, auth, req, opts); errExec != nil {
							t.Fatalf("Execute() error = %v", errExec)
						}
					}

					if got := ojson.GetBytes(capturedBody, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != level {
						t.Fatalf("thinkingLevel = %q, want %q; body=%s", got, level, capturedBody)
					}
					if budget := ojson.GetBytes(capturedBody, "request.generationConfig.thinkingConfig.thinkingBudget"); budget.Exists() {
						t.Fatalf("thinkingBudget should be absent, got %s; body=%s", budget.Raw, capturedBody)
					}
					if include := ojson.GetBytes(capturedBody, "request.generationConfig.thinkingConfig.includeThoughts"); !include.Exists() || !include.Bool() {
						t.Fatalf("includeThoughts = %s, want true; body=%s", include.Raw, capturedBody)
					}
				})
			}
		}
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

func TestAntigravityExecutorSynthesizedSessionIDScopedPerCredential(t *testing.T) {
	t.Parallel()

	negativeDecimal := regexp.MustCompile(`^-[0-9]+$`)
	executor := NewAntigravityExecutor(&config.Config{})

	captureSessionID := func(t *testing.T, authID, kind, model, userText string, metadata map[string]any) string {
		t.Helper()
		var capturedBody []byte
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			var errRead error
			capturedBody, errRead = io.ReadAll(req.Body)
			if errRead != nil {
				return nil, errRead
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":400,"message":"captured"}}`)),
			}, nil
		}))
		auth := &cliproxyauth.Auth{
			ID: authID,
			Metadata: map[string]any{
				"access_token": "token",
				"expired":      time.Now().Add(2 * time.Hour).Format(time.RFC3339),
				"project_id":   "project-1",
			},
		}
		req := cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"` + userText + `"}]}]}`),
		}
		opts := cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatGemini,
			ResponseFormat: sdktranslator.FormatGemini,
			Metadata:       metadata,
		}
		switch kind {
		case "execute":
			_, _ = executor.Execute(ctx, auth, req, opts)
		case "stream":
			_, _ = executor.ExecuteStream(ctx, auth, req, opts)
		default:
			t.Fatalf("unknown route kind %q", kind)
		}
		if len(capturedBody) == 0 {
			t.Fatalf("expected upstream request body to be captured for auth=%s kind=%s model=%s", authID, kind, model)
		}
		sessionID := ojson.GetBytes(capturedBody, "request.sessionId").String()
		if !negativeDecimal.MatchString(sessionID) {
			t.Fatalf("request.sessionId = %q, want negative int63 decimal", sessionID)
		}
		return sessionID
	}

	t.Run("canonical session stays stable across models and turns on same credential and rotates on failover", func(t *testing.T) {
		meta := map[string]any{
			cliproxyexecutor.CanonicalSessionIDMetadataKey: "claude:sess-7706",
		}
		authAGemini := captureSessionID(t, "auth-a", "execute", "gemini-3.6-flash-high", "first turn text", meta)
		authAClaude := captureSessionID(t, "auth-a", "execute", "claude-sonnet-4-6", "different compacted turn text", meta)
		authAStream := captureSessionID(t, "auth-a", "stream", "gemini-3.7-flash-high", "stream turn text", meta)
		if authAGemini != authAClaude || authAGemini != authAStream {
			t.Fatalf("same credential + canonical session produced unstable request.sessionId: gemini=%q claude=%q stream=%q", authAGemini, authAClaude, authAStream)
		}

		authBGemini := captureSessionID(t, "auth-b", "execute", "gemini-3.6-flash-high", "first turn text", meta)
		authBClaude := captureSessionID(t, "auth-b", "execute", "claude-sonnet-4-6", "first turn text", meta)
		authBStream := captureSessionID(t, "auth-b", "stream", "gemini-3.6-flash-high", "first turn text", meta)
		if authBGemini == authAGemini {
			t.Fatalf("failover credential auth-b reused auth-a request.sessionId %q", authBGemini)
		}
		if authBGemini != authBClaude || authBGemini != authBStream {
			t.Fatalf("failover credential auth-b produced unstable request.sessionId across paths: gemini=%q claude=%q stream=%q", authBGemini, authBClaude, authBStream)
		}
	})

	t.Run("derived session rotates across credential failover", func(t *testing.T) {
		meta := map[string]any{
			cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:derived-7706",
		}
		authAFirst := captureSessionID(t, "auth-a", "execute", "gemini-3.6-flash-high", "prompt", meta)
		authASecond := captureSessionID(t, "auth-a", "stream", "claude-sonnet-4-6", "different prompt", meta)
		if authAFirst != authASecond {
			t.Fatalf("same credential + derived session changed: first=%q second=%q", authAFirst, authASecond)
		}
		authB := captureSessionID(t, "auth-b", "execute", "gemini-3.6-flash-high", "prompt", meta)
		if authB == authAFirst {
			t.Fatalf("failover credential auth-b reused auth-a derived request.sessionId %q", authB)
		}
	})

	t.Run("content fallback rotates across credential failover", func(t *testing.T) {
		authAFirst := captureSessionID(t, "auth-a", "execute", "gemini-3.6-flash-high", "shared first prompt", nil)
		authASecond := captureSessionID(t, "auth-a", "stream", "gemini-3.7-flash-high", "shared first prompt", nil)
		if authAFirst != authASecond {
			t.Fatalf("same credential + content fallback changed: first=%q second=%q", authAFirst, authASecond)
		}
		authB := captureSessionID(t, "auth-b", "execute", "gemini-3.6-flash-high", "shared first prompt", nil)
		if authB == authAFirst {
			t.Fatalf("failover credential auth-b reused auth-a content-fallback request.sessionId %q", authB)
		}
	})
}

// TestAntigravityPrepareRequestAuth_DiscoveryTransportErrorIsNotClientFault
// verifies that a project-discovery failure without an upstream HTTP status
// (for example a proxy connect error) is reported as a transient transport
// problem rather than an invalid-request 400, so the conductor does not halt
// credential rotation on a request that could succeed on another credential.
func TestAntigravityPrepareRequestAuth_DiscoveryTransportErrorIsNotClientFault(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		"access_token": "token",
		"expired":      time.Now().Add(1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("proxy connect: connection refused")
	}))

	_, err := executor.PrepareRequestAuth(ctx, auth)
	if err == nil {
		t.Fatalf("PrepareRequestAuth should fail on transport error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error should expose StatusCode(), got %T (%v)", err, err)
	}
	if got := status.StatusCode(); got == http.StatusBadRequest {
		t.Fatalf("status code = %d, transport failure must not masquerade as invalid-request 400", got)
	}
}

// TestAntigravityPrepareRequestAuth_MissingRefreshTokenIsNotUpstreamAttempt
// verifies that a credential missing a refresh token fails as a local
// preparation error and does not mark an upstream attempt, so it cannot
// outrank a genuine upstream rejection recorded earlier in the round.
func TestAntigravityPrepareRequestAuth_MissingRefreshTokenIsNotUpstreamAttempt(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		// access_token present but already expired and no refresh_token: the
		// credential fails locally before any HTTP request is issued.
		"access_token": "expired-token",
		"expired":      time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())

	_, err := executor.PrepareRequestAuth(ctx, auth)
	if err == nil {
		t.Fatalf("PrepareRequestAuth should fail without refresh token")
	}
	if cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatalf("missing refresh token must not mark an upstream attempt")
	}
}

// TestAntigravityPrepareRequestAuth_OAuthRefreshFailureMarksUpstreamAttempt
// verifies that a genuine upstream rejection during OAuth token refresh (e.g.
// a 403 from the token endpoint) marks the request's upstream-attempt tracker,
// so the conductor can attribute it as a real upstream error that outranks
// later local credential-preparation failures.
func TestAntigravityPrepareRequestAuth_OAuthRefreshFailureMarksUpstreamAttempt(t *testing.T) {
	executor := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{
		// expired access token + refresh token: forces the OAuth refresh path,
		// which performs a real upstream HTTP call that fails with 403.
		"access_token":  "expired-token",
		"refresh_token": "refresh-token",
		"expired":       time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access_denied"}`)),
		}, nil
	}))
	ctx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)

	_, err := executor.PrepareRequestAuth(ctx, auth)
	if err == nil {
		t.Fatalf("PrepareRequestAuth should fail on upstream 403 during refresh")
	}
	if !cliproxyexecutor.UpstreamAttempted(ctx) {
		t.Fatalf("upstream refresh 403 should mark the upstream attempt tracker")
	}
}

// TestAntigravityPrepareRequestAuth_SharedRefreshFailureMarksEachTracker
// verifies that when concurrent requests share one singleflight OAuth refresh
// that fails upstream, EACH request's own upstream-attempt tracker is marked —
// not only the tracker of the request that led the flight.
func TestAntigravityPrepareRequestAuth_SharedRefreshFailureMarksEachTracker(t *testing.T) {
	executor := &AntigravityExecutor{}
	newAuth := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "shared-refresh-token",
			"expired":       time.Now().Add(-1 * time.Hour).Format(time.RFC3339),
		}}
	}
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		// Slow enough that both callers join the same singleflight refresh.
		time.Sleep(50 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"access_denied"}`)),
		}, nil
	})
	newCtx := func() context.Context {
		c := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
		return cliproxyexecutor.WithUpstreamAttemptTracker(c)
	}

	ctxA, ctxB := newCtx(), newCtx()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = executor.PrepareRequestAuth(ctxA, newAuth()) }()
	go func() { defer wg.Done(); _, _ = executor.PrepareRequestAuth(ctxB, newAuth()) }()
	wg.Wait()

	if !cliproxyexecutor.UpstreamAttempted(ctxA) {
		t.Fatalf("request A tracker was not marked on shared upstream refresh failure")
	}
	if !cliproxyexecutor.UpstreamAttempted(ctxB) {
		t.Fatalf("request B tracker was not marked on shared upstream refresh failure")
	}
}
