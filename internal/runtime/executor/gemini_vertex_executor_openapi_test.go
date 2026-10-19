package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGeminiVertexOpenAPI_ServiceAccount_Execute(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotMethod string
	var gotAuth string
	var upstreamBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		upstreamBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vertex-maas-1",
			"object":"chat.completion",
			"created":1700000000,
			"model":"deepseek-ai/deepseek-v4.1-flash-maas",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello from vertex maas"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}
		}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})
	ctx = context.WithValue(ctx, "gin", ginCtx)

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-openapi",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"email":           "vertex-sa@example.iam.gserviceaccount.com",
			"service_account": sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{
		SDKConfig: config.SDKConfig{RequestLog: true},
	})
	req := cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hello maas"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	resp, errExecute := exec.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedPath := "/v1/projects/proxy-test/locations/global/endpoints/openapi/chat/completions"
	if gotPath != expectedPath {
		t.Fatalf("got path = %q, want %q", gotPath, expectedPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("got method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotAuth != "Bearer mock-vertex-sa-token" {
		t.Fatalf("got auth = %q, want Bearer mock-vertex-sa-token", gotAuth)
	}
	if gotModel := gjson.GetBytes(upstreamBody, "model").String(); gotModel != "deepseek-ai/deepseek-v4.1-flash-maas" {
		t.Fatalf("got upstream model = %q, want deepseek-ai/deepseek-v4.1-flash-maas; body=%s", gotModel, string(upstreamBody))
	}
	if gotMsg := gjson.GetBytes(upstreamBody, "messages.0.content").String(); gotMsg != "hello maas" {
		t.Fatalf("got upstream messages.0.content = %q, want hello maas; body=%s", gotMsg, string(upstreamBody))
	}
	if gotText := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); gotText != "hello from vertex maas" {
		t.Fatalf("got response content = %q, want hello from vertex maas; payload=%s", gotText, string(resp.Payload))
	}

	if gotType, gotVal := auth.AccountInfo(); gotType != "oauth" || gotVal != "vertex-sa@example.iam.gserviceaccount.com" {
		t.Fatalf("auth.AccountInfo() = (%q, %q), want (oauth, vertex-sa@example.iam.gserviceaccount.com)", gotType, gotVal)
	}
	recordedReq, okReq := ginCtx.Get("API_REQUEST")
	if !okReq {
		t.Fatal("expected API_REQUEST in gin context")
	}
	recordedStr := string(recordedReq.([]byte))
	if !strings.Contains(recordedStr, "type=oauth") {
		t.Fatalf("expected type=oauth in API_REQUEST log, got: %s", recordedStr)
	}
	if strings.Contains(recordedStr, "type=api_key") || strings.Contains(recordedStr, "mock-vertex-sa-token") {
		t.Fatalf("unexpected api_key auth type or raw token in API_REQUEST log: %s", recordedStr)
	}
}

func TestGeminiVertexOpenAPI_ModelNormalization_PublisherResourceAndExplicitOptIn(t *testing.T) {
	var mu sync.Mutex
	var upstreamModels []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamModels = append(upstreamModels, gjson.GetBytes(body, "model").String())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-norm","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	exec := NewGeminiVertexExecutor(&config.Config{})

	// 1. publishers/<pub>/models/<id> normalizes to <pub>/<id>
	saAuth := &cliproxyauth.Auth{
		ID:       "vertex-sa-norm-1",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"service_account": sa,
		},
	}
	_, errExec1 := exec.Execute(ctx, saAuth, cliproxyexecutor.Request{
		Model:   "publishers/deepseek-ai/models/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"publishers/deepseek-ai/models/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExec1 != nil {
		t.Fatalf("Execute(publishers/...) error = %v", errExec1)
	}

	// 2. Explicit openapi=true with bare model normalizes to google/<model>
	explicitAuth := &cliproxyauth.Auth{
		ID:       "vertex-sa-norm-2",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"openapi":         true,
			"service_account": sa,
		},
	}
	_, errExec2 := exec.Execute(ctx, explicitAuth, cliproxyexecutor.Request{
		Model:   "gemini-2.5-pro",
		Payload: []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExec2 != nil {
		t.Fatalf("Execute(explicit openapi=true) error = %v", errExec2)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(upstreamModels) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(upstreamModels))
	}
	if upstreamModels[0] != "deepseek-ai/deepseek-v4.1-flash-maas" {
		t.Fatalf("upstreamModels[0] = %q, want deepseek-ai/deepseek-v4.1-flash-maas", upstreamModels[0])
	}
	if upstreamModels[1] != "google/gemini-2.5-pro" {
		t.Fatalf("upstreamModels[1] = %q, want google/gemini-2.5-pro", upstreamModels[1])
	}
}

func TestGeminiVertexOpenAPI_ServiceAccount_ExecuteStream_AndOpenAPILocationOverride(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotAuth string
	var upstreamBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		upstreamBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-1\",\"object\":\"chat.completion.chunk\",\"model\":\"qwen/qwen3-coder-480b-a35b-instruct-maas\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"stream ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-stream-1\",\"object\":\"chat.completion.chunk\",\"model\":\"qwen/qwen3-coder-480b-a35b-instruct-maas\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2,\"total_tokens\":6}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-openapi-stream",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":       "proxy-test",
			"location":         "us-central1",
			"openapi_location": "global",
			"service_account":  sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "qwen/qwen3-coder-480b-a35b-instruct-maas",
		Payload: []byte(`{"model":"qwen/qwen3-coder-480b-a35b-instruct-maas","messages":[{"role":"user","content":"stream test"}],"stream":true}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}

	stream, errStream := exec.ExecuteStream(ctx, auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var output strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedPath := "/v1/projects/proxy-test/locations/global/endpoints/openapi/chat/completions"
	if gotPath != expectedPath {
		t.Fatalf("got path = %q, want %q", gotPath, expectedPath)
	}
	if gotAuth != "Bearer mock-vertex-sa-token" {
		t.Fatalf("got auth = %q, want Bearer mock-vertex-sa-token", gotAuth)
	}
	if !gjson.GetBytes(upstreamBody, "stream_options.include_usage").Bool() {
		t.Fatalf("expected stream_options.include_usage=true in upstream body: %s", string(upstreamBody))
	}
	if !strings.Contains(output.String(), "stream ok") {
		t.Fatalf("expected stream output to contain 'stream ok', got: %s", output.String())
	}
}

func TestGeminiVertexOpenAPI_PayloadRulesOrdering(t *testing.T) {
	var mu sync.Mutex
	var nonStreamBody []byte
	var streamBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Accept") == "text/event-stream" {
			mu.Lock()
			streamBody = body
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		mu.Lock()
		nonStreamBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	exec := NewGeminiVertexExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "deepseek-ai/deepseek-v4.1-flash-maas", Protocol: "openai"},
					},
					Params: map[string]any{
						"temperature":                  0.25,
						"custom_openapi_param":         "final_barrier_applied",
						"stream_options.include_usage": false,
					},
				},
			},
			Filter: []config.PayloadFilterRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "deepseek-ai/deepseek-v4.1-flash-maas", Protocol: "openai"},
					},
					Params: []string{"top_p"},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-openapi-rules",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"service_account": sa,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","temperature":0.9,"top_p":0.8,"messages":[{"role":"user","content":"hi"}]}`),
	}
	_, errExecute := exec.Execute(ctx, auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	streamRes, errStream := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	for name, body := range map[string][]byte{"Execute": nonStreamBody, "ExecuteStream": streamBody} {
		if got := gjson.GetBytes(body, "custom_openapi_param").String(); got != "final_barrier_applied" {
			t.Fatalf("%s: custom_openapi_param = %q, want final_barrier_applied; body=%s", name, got, string(body))
		}
		if got := gjson.GetBytes(body, "temperature").Float(); got != 0.25 {
			t.Fatalf("%s: temperature = %v, want 0.25; body=%s", name, got, string(body))
		}
		if gjson.GetBytes(body, "top_p").Exists() {
			t.Fatalf("%s: expected top_p to be filtered out, got body=%s", name, string(body))
		}
	}
	if gjson.GetBytes(streamBody, "stream_options.include_usage").Bool() {
		t.Fatalf("ExecuteStream: expected payload override stream_options.include_usage=false to win over built-in default, got body=%s", string(streamBody))
	}
}

func TestGeminiVertexOpenAPI_APIKey_ValidationAndThirdPartyRelay(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotBearer string
	var gotGoogKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotBearer = r.Header.Get("Authorization")
		gotGoogKey = r.Header.Get("x-goog-api-key")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_, _ = w.Write([]byte(`{"id":"chatcmpl-key","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"key openapi ok"},"finish_reason":"stop"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"responseId":"resp1","candidates":[{"content":{"parts":[{"text":"google native ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	exec := NewGeminiVertexExecutor(&config.Config{})

	// 1. Static API key targeting official Google Vertex host is rejected for endpoints/openapi
	for _, googleBase := range []string{"", "https://aiplatform.googleapis.com/v1/projects/proxy-test/locations/global"} {
		googleKeyAuth := &cliproxyauth.Auth{
			ID:       "vertex-apikey-google",
			Provider: "vertex",
			Attributes: map[string]string{
				"api_key":  "static-api-key",
				"base_url": googleBase,
			},
		}
		_, errGoogleKey := exec.Execute(context.Background(), googleKeyAuth, cliproxyexecutor.Request{
			Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
			Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hi"}]}`),
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatOpenAI,
			ResponseFormat: sdktranslator.FormatOpenAI,
		})
		if errGoogleKey == nil || !strings.Contains(errGoogleKey.Error(), "requires service account credentials") {
			t.Fatalf("Execute(googleBase=%q) error = %v, want requires service account credentials", googleBase, errGoogleKey)
		}
	}

	// 2. Third-party compatible gateway with custom base_url routes non-google publisher model to endpoints/openapi/chat/completions
	customGatewayAuth := &cliproxyauth.Auth{
		ID:       "vertex-apikey-custom-gateway",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL + "/v1/projects/proxy-test/locations/global",
		},
	}
	resp, errExecute := exec.Execute(context.Background(), customGatewayAuth, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errExecute != nil {
		t.Fatalf("Execute(maas) error = %v", errExecute)
	}
	mu.Lock()
	if gotPath != "/v1/projects/proxy-test/locations/global/endpoints/openapi/chat/completions" {
		mu.Unlock()
		t.Fatalf("got path = %q, want /v1/projects/proxy-test/locations/global/endpoints/openapi/chat/completions", gotPath)
	}
	if gotBearer != "Bearer test-vertex-key" {
		mu.Unlock()
		t.Fatalf("got Authorization = %q, want Bearer test-vertex-key", gotBearer)
	}
	mu.Unlock()
	if gotText := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); gotText != "key openapi ok" {
		t.Fatalf("got response = %q, want key openapi ok", gotText)
	}

	// 3. google/ prefix on a relay base URL keeps native generateContent path
	relayAuth := &cliproxyauth.Auth{
		ID:       "vertex-apikey-relay",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-relay-key",
			"base_url": server.URL,
		},
	}
	_, errRelay := exec.Execute(context.Background(), relayAuth, cliproxyexecutor.Request{
		Model:   "google/gemini-2.5-pro",
		Payload: []byte(`{"model":"google/gemini-2.5-pro","messages":[{"role":"user","content":"hi relay"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if errRelay != nil {
		t.Fatalf("Execute(google/gemini-2.5-pro) error = %v", errRelay)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/publishers/google/models/google/gemini-2.5-pro:generateContent" {
		t.Fatalf("got relay path = %q, want /v1/publishers/google/models/google/gemini-2.5-pro:generateContent", gotPath)
	}
	if gotGoogKey != "test-relay-key" {
		t.Fatalf("got x-goog-api-key = %q, want test-relay-key", gotGoogKey)
	}
}

func TestGeminiVertexOpenAPI_CountTokens(t *testing.T) {
	var hitUpstream bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitUpstream = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-openapi-count",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"service_account": sa,
		},
	}
	exec := NewGeminiVertexExecutor(&config.Config{})
	resp, errCount := exec.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"count these tokens"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if hitUpstream {
		t.Fatal("expected CountTokens for OpenAPI model to use local tokenizer without hitting upstream")
	}
	if gjson.GetBytes(resp.Payload, "totalTokens").Int() <= 0 {
		t.Fatalf("expected positive totalTokens, got: %s", string(resp.Payload))
	}
}
