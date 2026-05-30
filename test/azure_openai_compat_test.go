package test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAzureOpenAICompatOpenAINonStream(t *testing.T) {
	var gotPath string
	var gotAPIVersion string
	var gotAPIKey string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotAPIVersion = r.URL.Query().Get("api-version")
		gotAPIKey = r.Header.Get("api-key")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_azure","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"azure ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	executor := runtimeexecutor.NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	resp, err := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
		"deployment":  "prod-gpt-4-1",
	}}, cliproxyexecutor.Request{
		Model:   "azure-gpt-4.1",
		Payload: []byte(`{"model":"azure-gpt-4.1","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/openai/deployments/prod-gpt-4-1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotAPIVersion != "2025-04-01-preview" {
		t.Fatalf("api-version = %q", gotAPIVersion)
	}
	if gotAPIKey != "azure-key" {
		t.Fatalf("api-key = %q", gotAPIKey)
	}
	if gotModel := gjson.GetBytes(gotBody, "model").String(); gotModel != "prod-gpt-4-1" {
		t.Fatalf("upstream body model = %q", gotModel)
	}
	if object := gjson.GetBytes(resp.Payload, "object").String(); object != "chat.completion" {
		t.Fatalf("response object = %q", object)
	}
}

func TestAzureOpenAICompatClaudeNonStream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_azure","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"azure ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	executor := runtimeexecutor.NewAzureOpenAIExecutor("azure-openai", &config.Config{})
	resp, err := executor.Execute(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL,
		"api_version": "2025-04-01-preview",
		"api_key":     "azure-key",
	}}, cliproxyexecutor.Request{
		Model:   "prod-claude-facing-deployment",
		Payload: []byte(`{"model":"claude-facing-model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: []byte(`{"model":"claude-facing-model","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotMessages := gjson.GetBytes(gotBody, "messages").Array(); len(gotMessages) != 1 {
		t.Fatalf("upstream messages length = %d", len(gotMessages))
	}
	if gotModel := gjson.GetBytes(gotBody, "model").String(); gotModel != "prod-claude-facing-deployment" {
		t.Fatalf("upstream body model = %q", gotModel)
	}
	if responseType := gjson.GetBytes(resp.Payload, "type").String(); responseType != "message" {
		t.Fatalf("response type = %q", responseType)
	}
}
