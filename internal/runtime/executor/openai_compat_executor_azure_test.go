package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorAzureChatCompletions(t *testing.T) {
	requests := make(chan *http.Request, 2)
	bodies := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- r.Clone(context.Background())
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		if gjson.GetBytes(body, "stream").Bool() {
			_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "azure-openai",
		BaseURL: server.URL + "/gateway?tenant=one",
		Azure: &config.OpenAICompatibilityAzure{
			Deployment: "production/chat v1",
			APIVersion: "2025-04-01 preview",
		},
	}}}
	executor := NewOpenAICompatExecutor("openai-compatible-azure-openai", cfg)
	auth := &cliproxyauth.Auth{Provider: "openai-compatible-azure-openai", Attributes: map[string]string{
		"base_url":     server.URL + "/gateway?tenant=one",
		"api_key":      "azure-secret",
		"config_index": "0",
	}}
	request := cliproxyexecutor.Request{
		Model:   "upstream-model",
		Payload: []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}

	if _, err := executor.Execute(context.Background(), auth, request, options); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	assertAzureOpenAIRequest(t, <-requests, <-bodies)

	options.Stream = true
	request.Payload = []byte(`{"model":"upstream-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	stream, err := executor.ExecuteStream(context.Background(), auth, request, options)
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	assertAzureOpenAIRequest(t, <-requests, <-bodies)
}

func assertAzureOpenAIRequest(t *testing.T, req *http.Request, body []byte) {
	t.Helper()
	wantRequestURI := "/gateway/openai/deployments/production%2Fchat%20v1/chat/completions?api-version=2025-04-01+preview&tenant=one"
	if req.RequestURI != wantRequestURI {
		t.Fatalf("RequestURI = %q, want %q", req.RequestURI, wantRequestURI)
	}
	if got := req.Header.Get("api-key"); got != "azure-secret" {
		t.Fatalf("api-key = %q, want azure-secret", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "upstream-model" {
		t.Fatalf("request model = %q, want upstream-model", got)
	}
	t.Logf("request evidence: %s api-key=%q authorization=%q model=%q", req.RequestURI, req.Header.Get("api-key"), req.Header.Get("Authorization"), gjson.GetBytes(body, "model").String())
}

func TestOpenAICompatExecutorStandardChatCompletionsRegression(t *testing.T) {
	requestSeen := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestSeen <- r.Clone(context.Background())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatible-standard", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "standard-secret"}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "standard-model",
		Payload: []byte(`{"model":"standard-model","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	got := <-requestSeen
	if got.RequestURI != "/v1/chat/completions" {
		t.Fatalf("RequestURI = %q, want /v1/chat/completions", got.RequestURI)
	}
	if authHeader := got.Header.Get("Authorization"); authHeader != "Bearer standard-secret" {
		t.Fatalf("Authorization = %q, want Bearer standard-secret", authHeader)
	}
	if apiKey := got.Header.Get("api-key"); apiKey != "" {
		t.Fatalf("api-key = %q, want empty", apiKey)
	}
}
