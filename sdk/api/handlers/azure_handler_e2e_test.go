package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	claudehandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestAzureOpenAIChatCompletionsHandlerRoutesAliasToDeployment(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		provider   = "azure-openai-handler-chat"
		authID     = "azure-openai-handler-chat-auth"
		modelAlias = "azure-openai-handler-chat-alias"
		deployment = "gpt-4o-handler-chat"
	)

	var gotPath string
	var gotRawQuery string
	var gotAPIKey string
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotAPIKey = r.Header.Get("api-key")
		gotAuthorization = r.Header.Get("Authorization")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_handler_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"azure ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAzureOpenAIExecutor(provider, &config.Config{}))
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":    upstream.URL,
			"api_version": "2024-10-21",
			"api_key":     "test-azure-key",
			"deployment":  deployment,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelAlias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := openaihandlers.NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+modelAlias+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	wantPath := "/openai/deployments/" + deployment + "/chat/completions"
	if gotPath != wantPath {
		t.Fatalf("upstream path = %q, want %q", gotPath, wantPath)
	}
	if gotRawQuery != "api-version=2024-10-21" {
		t.Fatalf("upstream query = %q, want api-version=2024-10-21", gotRawQuery)
	}
	if gotAPIKey != "test-azure-key" {
		t.Fatalf("api-key header = %q, want test-azure-key", gotAPIKey)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization header = %q, want empty", gotAuthorization)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != deployment {
		t.Fatalf("upstream model = %q, want %q; body=%s", got, deployment, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != "hi" {
		t.Fatalf("upstream message content = %q, want hi; body=%s", got, string(gotBody))
	}
	if got := gjson.Get(resp.Body.String(), "choices.0.message.content").String(); got != "azure ok" {
		t.Fatalf("response content = %q, want azure ok; body=%s", got, resp.Body.String())
	}
}

func TestAzureOpenAIClaudeMessagesHandlerTranslatesThroughAzure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		provider   = "azure-openai-handler-claude"
		authID     = "azure-openai-handler-claude-auth"
		modelAlias = "azure-openai-handler-claude-alias"
		deployment = "gpt-4o-handler-claude"
	)

	var gotPath string
	var gotAPIKey string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("api-key")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_handler_2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from azure"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewAzureOpenAIExecutor(provider, &config.Config{}))
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url":    upstream.URL,
			"api_version": "2024-10-21",
			"api_key":     "test-azure-key",
			"deployment":  deployment,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelAlias, Type: "claude", UserDefined: true}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := claudehandlers.NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+modelAlias+`","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	wantPath := "/openai/deployments/" + deployment + "/chat/completions"
	if gotPath != wantPath {
		t.Fatalf("upstream path = %q, want %q", gotPath, wantPath)
	}
	if gotAPIKey != "test-azure-key" {
		t.Fatalf("api-key header = %q, want test-azure-key", gotAPIKey)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != deployment {
		t.Fatalf("upstream model = %q, want %q; body=%s", got, deployment, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content").String(); got != "hi" {
		t.Fatalf("translated upstream content = %q, want hi; body=%s", got, string(gotBody))
	}
	if got := gjson.Get(resp.Body.String(), "content.0.text").String(); got != "hello from azure" {
		t.Fatalf("Claude response text = %q, want hello from azure; body=%s", got, resp.Body.String())
	}
	if got := gjson.Get(resp.Body.String(), "type").String(); got != "message" {
		t.Fatalf("Claude response type = %q, want message; body=%s", got, resp.Body.String())
	}
}
