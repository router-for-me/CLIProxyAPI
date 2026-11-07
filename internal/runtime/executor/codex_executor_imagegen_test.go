package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexExecutorExecuteResponsesLiteHeaderDoesNotInjectImageGenerationTool(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if tools := ojson.GetBytes(gotBody, "tools"); tools.Exists() {
		t.Fatalf("unexpected tools in responses-lite upstream payload: %s", tools.Raw)
	}
	parallelToolCalls := ojson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestCodexExecutorExecuteStreamResponsesLiteHeaderForcesParallelToolCallsFalse(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":   "test",
			"base_url":  server.URL,
			"plan_type": "pro",
		},
	}
	headers := make(http.Header)
	headers.Set(codexResponsesLiteHeader, "true")

	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      headers,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	parallelToolCalls := ojson.GetBytes(gotBody, "parallel_tool_calls")
	if !parallelToolCalls.Exists() || parallelToolCalls.Bool() {
		t.Fatalf("responses-lite parallel_tool_calls should be false: %s", gotBody)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},"input":[{"role":"user","content":"hello"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
	if ojson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no injected tools for responses-lite request, got %s", ojson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteBooleanMetadataDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":true},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteHeaderDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)
	headers := make(http.Header)
	headers.Set("X-OpenAI-Internal-Codex-Responses-Lite", "true")
	result := ensureImageGenerationTool(body, "gpt-5.6-sol", nil, headers)

	if string(result) != string(body) {
		t.Fatalf("expected responses-lite body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteFalseMetadataStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"},"input":"hello"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if got := ojson.GetBytes(result, "tools.0.type").String(); got != "image_generation" {
		t.Fatalf("tools.0.type = %q, want image_generation; body=%s", got, result)
	}

	for _, liteModel := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		liteBody := []byte(`{"model":"` + liteModel + `","client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"false"},"input":"hello"}`)
		liteHeaders := make(http.Header)
		liteHeaders.Set(codexResponsesLiteHeader, "false")
		liteResult := ensureImageGenerationTool(liteBody, liteModel, nil, liteHeaders)
		if ojson.GetBytes(liteResult, "tools").Exists() {
			t.Fatalf("for %s: expected no auto-injected tools even when responses-lite is explicitly false, got %s", liteModel, liteResult)
		}
	}
}

func TestEnsureImageGenerationTool_NoTools(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools")
	if !tools.IsArray() {
		t.Fatalf("expected tools array, got %v", tools.Type)
	}
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
	if arr[0].Get("output_format").String() != "png" {
		t.Fatalf("expected output_format=png, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ExistingToolsWithoutImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"get_weather","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "function" {
		t.Fatalf("expected first tool type=function, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_AlreadyPresent(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"f1"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools (no duplicate), got %d", len(arr))
	}
	if arr[0].Get("output_format").String() != "webp" {
		t.Fatalf("expected original output_format=webp preserved, got %s", arr[0].Get("output_format").String())
	}
}

func TestEnsureImageGenerationTool_ImageGenNamespaceDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_FlattenedImageGenFunctionDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"function","name":"image_gen.imagegen","parameters":{}}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
}

func TestEnsureImageGenerationTool_SimilarNamespaceStillInjectsTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"image_tools","tools":[{"type":"function","name":"imagegen","parameters":{}}]}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", tools[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_EmptyToolsArray(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "image_generation" {
		t.Fatalf("expected type=image_generation, got %s", arr[0].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_WebSearchAndImageGen(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","tools":[{"type":"web_search"}]}`)
	result := ensureImageGenerationTool(body, "gpt-5.4", nil, nil)

	tools := ojson.GetBytes(result, "tools")
	arr := tools.Array()
	if len(arr) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(arr))
	}
	if arr[0].Get("type").String() != "web_search" {
		t.Fatalf("expected first tool type=web_search, got %s", arr[0].Get("type").String())
	}
	if arr[1].Get("type").String() != "image_generation" {
		t.Fatalf("expected second tool type=image_generation, got %s", arr[1].Get("type").String())
	}
}

func TestEnsureImageGenerationTool_GPT53CodexSparkDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","input":"draw a cat"}`)
	result := ensureImageGenerationTool(body, "gpt-5.3-codex-spark", nil, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if ojson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for gpt-5.3-codex-spark, got %s", ojson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_FreeCodexAuthDoesNotInjectTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":"draw a cat"}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}
	result := ensureImageGenerationTool(body, "gpt-5.4", freeAuth, nil)

	if string(result) != string(body) {
		t.Fatalf("expected body to be unchanged, got %s", string(result))
	}
	if ojson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no tools for free codex auth, got %s", ojson.GetBytes(result, "tools").Raw)
	}
}

func TestEnsureImageGenerationTool_ResponsesLiteModelsDoNotInjectToolByDefault(t *testing.T) {
	for _, model := range []string{
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6.1-sol",
		"gpt-6.1-terra",
		"gpt-6-luna",
		"gpt-6-chat-thinking",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-reserve",
		"codex-auto-review",
		"codex/gpt-6-astra(medium)",
		"codex/gpt-5.6-sol(high)",
	} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","input":"hello"}`)
			result := ensureImageGenerationTool(body, model, nil, nil)
			if ojson.GetBytes(result, "tools").Exists() {
				t.Fatalf("expected no auto-injected tools for %s, got %s", model, result)
			}
		})
	}

	for _, nonLiteModel := range []string{"gpt-5.5", "gpt-5.4"} {
		t.Run("non-lite/"+nonLiteModel, func(t *testing.T) {
			body := []byte(`{"model":"` + nonLiteModel + `","input":"hello"}`)
			result := ensureImageGenerationTool(body, nonLiteModel, nil, nil)
			if got := ojson.GetBytes(result, "tools.0.type").String(); got != "image_generation" {
				t.Fatalf("for %s: tools.0.type = %q, want image_generation; body=%s", nonLiteModel, got, result)
			}
		})
	}
}

func TestCodexExecutorHostedImageGenerationSIWCAndAPIKeyPreserveUpstreamModelAndParams(t *testing.T) {
	for _, credKind := range []string{"api_key", "siwc"} {
		for _, transport := range []string{"http", "websocket"} {
			for _, stream := range []bool{false, true} {
				streamLabel := "false"
				if stream {
					streamLabel = "true"
				}
				t.Run(credKind+"/"+transport+"/stream="+streamLabel, func(t *testing.T) {
					capturedBody := make(chan []byte, 1)
					completedEvent := []byte(`{"type":"response.completed","response":{"id":"resp_siwc_apikey_7270","object":"response","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`)

					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if transport == "websocket" {
							upgrader := websocket.Upgrader{}
							conn, errUpgrade := upgrader.Upgrade(w, r, nil)
							if errUpgrade != nil {
								t.Errorf("upgrade websocket: %v", errUpgrade)
								return
							}
							defer func() {
								_ = conn.Close()
							}()
							_, msg, errRead := conn.ReadMessage()
							if errRead != nil {
								t.Errorf("read websocket message: %v", errRead)
								return
							}
							capturedBody <- msg
							if errWrite := conn.WriteMessage(websocket.TextMessage, completedEvent); errWrite != nil {
								t.Errorf("write websocket message: %v", errWrite)
							}
							return
						}
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Errorf("read http body: %v", errRead)
							return
						}
						capturedBody <- body
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write(append(append([]byte("data: "), completedEvent...), '\n', '\n'))
					}))
					defer server.Close()

					cfg := &config.Config{}
					var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if transport == "websocket" {
						exec = NewCodexWebsocketsExecutor(cfg)
					}
					auth := &cliproxyauth.Auth{
						ID:       t.Name(),
						Provider: "codex",
						Attributes: map[string]string{
							"base_url":  server.URL,
							"plan_type": "pro",
						},
					}
					if credKind == "api_key" {
						auth.Attributes["api_key"] = "sk-custom-key"
					} else {
						auth.Metadata = map[string]any{
							"access_token": "outer-token",
							"account_id":   "acct-1",
							"use_siwc":     true,
							"siwc": map[string]any{
								"access_token": "siwc-token",
								"base_url":     server.URL,
							},
						}
					}

					payload := []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"medium"},"input":"画一只小猫咪。","stream":true,"tools":[{"type":"image_generation","output_format":"png","complexity":"high"}],"tool_choice":{"type":"image_generation"}}`)
					req := cliproxyexecutor.Request{
						Model:   "gpt-6-astra",
						Payload: payload,
					}
					opts := cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FormatOpenAIResponse,
						Stream:       stream,
						Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
					}
					if stream {
						res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
						}
					} else {
						if _, errExec := exec.Execute(context.Background(), auth, req, opts); errExec != nil {
							t.Fatalf("Execute() error = %v", errExec)
						}
					}

					gotBody := <-capturedBody
					if gotModel := ojson.GetBytes(gotBody, "model").String(); gotModel != "gpt-6-astra" {
						t.Fatalf("%s upstream model = %q, want gpt-6-astra; body=%s", credKind, gotModel, gotBody)
					}
					if gotComplexity := ojson.GetBytes(gotBody, "tools.0.complexity").String(); gotComplexity != "high" {
						t.Fatalf("%s tools.0.complexity = %q, want high; body=%s", credKind, gotComplexity, gotBody)
					}
				})
			}
		}
	}
}

func TestCodexExecutorHostedImageGenerationPassthroughPreservesModelAndToolParams(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			streamLabel := "false"
			if stream {
				streamLabel = "true"
			}
			t.Run(transport+"/stream="+streamLabel, func(t *testing.T) {
				capturedBody := make(chan []byte, 1)
				capturedHint := make(chan string, 1)
				completedEvent := []byte(`{"type":"response.completed","response":{"id":"resp_passthrough_7270","object":"response","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}}`)

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedHint <- r.Header.Get(codexRoutingHintHeader)
					if transport == "websocket" {
						upgrader := websocket.Upgrader{}
						conn, errUpgrade := upgrader.Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Errorf("upgrade websocket: %v", errUpgrade)
							return
						}
						defer func() {
							_ = conn.Close()
						}()
						_, msg, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Errorf("read websocket message: %v", errRead)
							return
						}
						capturedBody <- msg
						if errWrite := conn.WriteMessage(websocket.TextMessage, completedEvent); errWrite != nil {
							t.Errorf("write websocket message: %v", errWrite)
						}
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read http body: %v", errRead)
						return
					}
					capturedBody <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(append(append([]byte("data: "), completedEvent...), '\n', '\n'))
				}))
				defer server.Close()

				cfg := &config.Config{
					SDKConfig: config.SDKConfig{
						DisableImageGeneration: config.DisableImageGenerationPassthrough,
					},
				}
				var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
				if transport == "websocket" {
					exec = NewCodexWebsocketsExecutor(cfg)
				}
				auth := &cliproxyauth.Auth{
					ID:       t.Name(),
					Provider: "codex",
					Attributes: map[string]string{
						"base_url":  server.URL,
						"plan_type": "pro",
					},
					Metadata: map[string]any{
						"access_token": "test-token",
						"account_id":   "acct-1",
					},
				}
				payload := []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"max"},"input":"画一只小猫咪。","stream":true,"tools":[{"type":"image_generation","output_format":"png","complexity":"high"}],"tool_choice":{"type":"image_generation"}}`)
				req := cliproxyexecutor.Request{
					Model:   "gpt-6-astra",
					Payload: payload,
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAIResponse,
					Stream:       stream,
				}
				if stream {
					res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error = %v", chunk.Err)
						}
					}
				} else {
					if _, errExec := exec.Execute(context.Background(), auth, req, opts); errExec != nil {
						t.Fatalf("Execute() error = %v", errExec)
					}
				}

				gotBody := <-capturedBody
				gotHint := <-capturedHint
				if gotModel := ojson.GetBytes(gotBody, "model").String(); gotModel != "gpt-6-astra" {
					t.Fatalf("passthrough upstream model = %q, want gpt-6-astra; body=%s", gotModel, gotBody)
				}
				if gotHint != "model=gpt-6-astra" {
					t.Fatalf("passthrough X-Codex-Routing-Hint = %q, want model=gpt-6-astra", gotHint)
				}
				if gotComplexity := ojson.GetBytes(gotBody, "tools.0.complexity").String(); gotComplexity != "high" {
					t.Fatalf("passthrough tools.0.complexity = %q, want high; body=%s", gotComplexity, gotBody)
				}
				if gotEffort := ojson.GetBytes(gotBody, "reasoning.effort").String(); gotEffort != "max" {
					t.Fatalf("passthrough reasoning.effort = %q, want max; body=%s", gotEffort, gotBody)
				}
			})
		}
	}
}

func TestCodexExecutorOptionalImageGenerationPreservesChatModelAndGuidesOnAutoTrigger(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			streamLabel := "false"
			if stream {
				streamLabel = "true"
			}
			t.Run("normal_chat_with_optional_tool/"+transport+"/stream="+streamLabel, func(t *testing.T) {
				capturedBody := make(chan []byte, 1)
				capturedHint := make(chan string, 1)
				completedEvent := []byte(`{"type":"response.completed","response":{"id":"resp_chat_7270","object":"response","status":"completed","model":"gpt-6-astra","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello!"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)

				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedHint <- r.Header.Get(codexRoutingHintHeader)
					if transport == "websocket" {
						upgrader := websocket.Upgrader{}
						conn, errUpgrade := upgrader.Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Errorf("upgrade websocket: %v", errUpgrade)
							return
						}
						defer func() {
							_ = conn.Close()
						}()
						_, msg, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Errorf("read websocket message: %v", errRead)
							return
						}
						capturedBody <- msg
						if errWrite := conn.WriteMessage(websocket.TextMessage, completedEvent); errWrite != nil {
							t.Errorf("write websocket message: %v", errWrite)
						}
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read http body: %v", errRead)
						return
					}
					capturedBody <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(append(append([]byte("data: "), completedEvent...), '\n', '\n'))
				}))
				defer server.Close()

				cfg := &config.Config{}
				var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
				if transport == "websocket" {
					exec = NewCodexWebsocketsExecutor(cfg)
				}
				auth := &cliproxyauth.Auth{
					ID:       t.Name(),
					Provider: "codex",
					Attributes: map[string]string{
						"base_url":  server.URL,
						"plan_type": "pro",
					},
					Metadata: map[string]any{
						"access_token": "test-token",
						"account_id":   "acct-1",
					},
				}
				req := cliproxyexecutor.Request{
					Model:   "gpt-6-astra",
					Payload: []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"medium"},"input":"Hello","stream":true,"tools":[{"type":"image_generation","output_format":"png","complexity":"high"}],"tool_choice":"auto"}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAIResponse,
					Stream:       stream,
				}
				if stream {
					res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					for chunk := range res.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error = %v", chunk.Err)
						}
					}
				} else {
					if _, errExec := exec.Execute(context.Background(), auth, req, opts); errExec != nil {
						t.Fatalf("Execute() error = %v", errExec)
					}
				}

				gotBody := <-capturedBody
				gotHint := <-capturedHint
				if gotModel := ojson.GetBytes(gotBody, "model").String(); gotModel != "gpt-6-astra" {
					t.Fatalf("upstream model = %q, want gpt-6-astra; body=%s", gotModel, gotBody)
				}
				if gotHint != "model=gpt-6-astra" {
					t.Fatalf("X-Codex-Routing-Hint = %q, want model=gpt-6-astra", gotHint)
				}
				if ojson.GetBytes(gotBody, "tools.0.complexity").Exists() {
					t.Fatalf("tools.0.complexity should be stripped; body=%s", gotBody)
				}
			})

		}
	}
}

func TestCodexTerminalFailureStatus_ImageGenerationUserErrorMapsToBadRequest(t *testing.T) {
	failedEvent := []byte(`{"type":"response.failed","response":{"status":"failed","error":{"type":"image_generation_user_error","code":"invalid_value","message":"Complexity 'medium' is not supported for this image model.","param":"complexity"}}}`)
	errTerminal, _, ok := codexTerminalFailureErrWithCooling(failedEvent, false)
	if !ok {
		t.Fatal("expected terminal error for response.failed")
	}
	if !strings.Contains(errTerminal.Error(), "Complexity 'medium' is not supported for this image model.") {
		t.Fatalf("unexpected error message: %v", errTerminal)
	}
	if !strings.Contains(errTerminal.Error(), "/v1/images/generations") || !strings.Contains(errTerminal.Error(), "gpt-5.4-mini") {
		t.Fatalf("expected remediation guidance in error message, got: %v", errTerminal)
	}
	if got := errTerminal.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("StatusCode() = %d, want %d (%s)", got, http.StatusBadRequest, errTerminal.Error())
	}
}
