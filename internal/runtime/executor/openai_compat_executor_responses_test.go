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

func TestOpenAICompatExecutorResponsesBackendPreservesNativeXSearch(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAuth string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"model\":\"grok-4.6\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{
		XAI: config.XAIConfig{InjectXSearch: true},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:       "grok",
			APIBackend: openAICompatResponsesBackend,
		}},
	}
	exec := NewOpenAICompatExecutor("openai-compatible-grok", cfg)
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatible-grok",
		Attributes: map[string]string{
			"api_key":     "upstream-key",
			"base_url":    server.URL + "/v1",
			"compat_name": "grok",
		},
	}

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","input":"search X"}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotAuth != "Bearer upstream-key" {
		t.Fatalf("Authorization = %q, want Bearer upstream-key", gotAuth)
	}
	xSearchCount := 0
	for _, tool := range gjson.GetBytes(gotBody, "tools").Array() {
		if tool.Get("type").String() == "x_search" {
			xSearchCount++
		}
	}
	if xSearchCount != 1 {
		t.Fatalf("x_search tool count = %d, want 1; body=%s", xSearchCount, gotBody)
	}
}

func TestOpenAICompatExecutorUsesResponsesBackendOnlyForResponsesRequests(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:       "grok",
		APIBackend: " ReSpOnSeS ",
	}}}
	exec := NewOpenAICompatExecutor("openai-compatible-grok", cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"compat_name": "grok"}}

	if !exec.usesResponsesBackend(auth, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}) {
		t.Fatal("Responses request did not select the responses backend")
	}
	if exec.usesResponsesBackend(auth, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}) {
		t.Fatal("Chat Completions request unexpectedly selected the responses backend")
	}
}
