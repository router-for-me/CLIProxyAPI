package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const azureTestChatCompletion = `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func TestAzureOpenAIChatCompletionsURL(t *testing.T) {
	got, err := azureOpenAIChatCompletionsURL("https://example.openai.azure.com/base?existing=1", "deploy/name", "2025-04-01-preview")
	if err != nil {
		t.Fatalf("azureOpenAIChatCompletionsURL error: %v", err)
	}
	want := "https://example.openai.azure.com/base/openai/deployments/deploy%2Fname/chat/completions?api-version=2025-04-01-preview&existing=1"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestAzureOpenAIChatCompletionsURLV1(t *testing.T) {
	got, err := buildAzureChatCompletionsURL(azureOpenAIOptions{
		Endpoint: "https://example.openai.azure.com/base?existing=1",
		PathMode: azureOpenAIPathModeV1,
	})
	if err != nil {
		t.Fatalf("buildAzureChatCompletionsURL v1 error: %v", err)
	}
	want := "https://example.openai.azure.com/base/openai/v1/chat/completions?existing=1"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestAzureOpenAIChatCompletionsURLValidation(t *testing.T) {
	tests := []struct {
		name string
		opts azureOpenAIOptions
	}{
		{name: "missing endpoint", opts: azureOpenAIOptions{Deployment: "dep", APIVersion: "2025-04-01-preview"}},
		{name: "missing api version", opts: azureOpenAIOptions{Endpoint: "https://example.openai.azure.com", Deployment: "dep", PathMode: azureOpenAIPathModeDeployment}},
		{name: "unsupported path mode", opts: azureOpenAIOptions{Endpoint: "https://example.openai.azure.com", Deployment: "dep", APIVersion: "2025-04-01-preview", PathMode: "bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildAzureChatCompletionsURL(tt.opts); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestAzureOpenAIExecutorUsesDeploymentURLAndAPIKey(t *testing.T) {
	var gotPath string
	var gotQuery string
	var gotAPIKey string
	var gotAuthorization string
	var gotCustomHeader string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.Query().Get("api-version")
		gotAPIKey = r.Header.Get("api-key")
		gotAuthorization = r.Header.Get("Authorization")
		gotCustomHeader = r.Header.Get("X-Custom-Header")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(azureTestChatCompletion))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":               server.URL,
		"api_version":            "2025-04-01-preview",
		"api_key":                "azure-key",
		"deployment":             "deployment-one",
		"header:X-Custom-Header": "custom-value",
	}}
	payload := []byte(`{"model":"ignored-alias","messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "alias-model",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/openai/deployments/deployment-one/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "2025-04-01-preview" {
		t.Fatalf("api-version = %q", gotQuery)
	}
	if gotAPIKey != "azure-key" {
		t.Fatalf("api-key = %q", gotAPIKey)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization should be empty, got %q", gotAuthorization)
	}
	if gotCustomHeader != "custom-value" {
		t.Fatalf("X-Custom-Header = %q", gotCustomHeader)
	}
	if gotModel := gjson.GetBytes(gotBody, "model").String(); gotModel != "deployment-one" {
		t.Fatalf("body model = %q, want deployment-one", gotModel)
	}
}

func TestAzureOpenAIExecutorUsesAADBearerFromAPIKeyWhenAuthTypeAAD(t *testing.T) {
	var gotAuthorization string
	var gotAPIKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(azureTestChatCompletion))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "aad-token",
		"auth_type":   "aad",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotAuthorization != "Bearer aad-token" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotAPIKey != "" {
		t.Fatalf("api-key should be empty, got %q", gotAPIKey)
	}
}

func TestAzureOpenAIExecutorUsesV1PathWithoutAPIVersion(t *testing.T) {
	var gotPath string
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(azureTestChatCompletion))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":  server.URL,
		"api_key":   "azure-key",
		"path_mode": "v1",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/openai/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestAzureOpenAIExecutorStreamsSSEAndRequestsUsage(t *testing.T) {
	var gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if !gjson.GetBytes(gotBody, "stream_options.include_usage").Bool() {
		t.Fatalf("stream_options.include_usage not set in body: %s", string(gotBody))
	}
	if len(chunks) == 0 || !strings.Contains(strings.Join(chunks, ""), "ok") {
		t.Fatalf("expected translated SSE chunks, got %#v", chunks)
	}
}

func TestAzureOpenAIExecutorStreamCanDisableUsage(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":      server.URL,
		"api_version":   "2025-04-01-preview",
		"api_key":       "azure-key",
		"include_usage": "false",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if gjson.GetBytes(gotBody, "stream_options.include_usage").Exists() {
		t.Fatalf("stream_options.include_usage should not be set in body: %s", string(gotBody))
	}
}

func TestAzureOpenAIChatCompletionsURLPreservesEndpointQuerySlash(t *testing.T) {
	got, err := buildAzureChatCompletionsURL(azureOpenAIOptions{
		Endpoint:   "https://example.openai.azure.com/base?existing=/",
		Deployment: "deployment-one",
		APIVersion: "2025-04-01-preview",
		PathMode:   azureOpenAIPathModeDeployment,
	})
	if err != nil {
		t.Fatalf("buildAzureChatCompletionsURL error: %v", err)
	}
	want := "https://example.openai.azure.com/base/openai/deployments/deployment-one/chat/completions?api-version=2025-04-01-preview&existing=%2F"
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestAzureOpenAIExecutorStreamDoesNotEmitExtraPayloadAfterDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, string(chunk.Payload))
	}
	if len(chunks) != 1 || !strings.Contains(chunks[0], "ok") {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestAzureOpenAIExecutorStreamDataErrorReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"code\":\"rate_limit_exceeded\",\"message\":\"Rate limit exceeded\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var gotErr error
	var joined strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			continue
		}
		joined.Write(chunk.Payload)
	}
	if gotErr == nil {
		t.Fatalf("expected stream error")
	}
	if !strings.Contains(gotErr.Error(), "rate_limit_exceeded") {
		t.Fatalf("error = %q", gotErr.Error())
	}
	if strings.Contains(joined.String(), "[DONE]") {
		t.Fatalf("unexpected synthetic DONE after error: %q", joined.String())
	}
}

func TestAzureOpenAIExecutorPreservesErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded","type":"too_many_requests","inner_error":{"code":"TokensPerMinuteExceeded"}}}`))
	}))
	defer server.Close()

	executor := NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deployment-one",
		Payload: []byte(`{"model":"deployment-one","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatalf("expected error")
	}
	errText := err.Error()
	for _, want := range []string{"rate_limit_exceeded", "Rate limit exceeded", "TokensPerMinuteExceeded"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q does not contain %q", errText, want)
		}
	}
}
