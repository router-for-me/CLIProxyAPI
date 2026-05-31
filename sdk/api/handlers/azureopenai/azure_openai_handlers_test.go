package azureopenai

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
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestAzureOpenAIDeploymentChatCompletionsRoutesToOpenAICompat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		provider = "azure-ingress-openai"
		authID   = "azure-ingress-openai-auth"
		alias    = "azure-ingress-openai-alias"
	)

	var gotPath string
	var gotRawQuery string
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_azure_ingress_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"openai ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, &config.Config{}))
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: provider,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": upstream.URL,
			"api_key":  "openai-upstream-key",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	router := newAzureOpenAITestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+alias+"/chat/completions?api-version=2024-10-21", strings.NewReader(`{"model":"conflicting-client-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("upstream path = %q, want /chat/completions", gotPath)
	}
	if gotRawQuery != "" {
		t.Fatalf("upstream query = %q, want empty", gotRawQuery)
	}
	if gotAuthorization != "Bearer openai-upstream-key" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != alias {
		t.Fatalf("upstream model = %q, want %q; body=%s", got, alias, string(gotBody))
	}
	if got := gjson.Get(resp.Body.String(), "choices.0.message.content").String(); got != "openai ok" {
		t.Fatalf("response content = %q, want openai ok; body=%s", got, resp.Body.String())
	}
}

func TestAzureOpenAIV1ChatCompletionsRoutesToOpenAICompat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		provider = "azure-ingress-openai-v1"
		authID   = "azure-ingress-openai-v1-auth"
		alias    = "azure-ingress-openai-v1-alias"
	)

	var gotPath string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_azure_ingress_2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"v1 ok"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, &config.Config{}))
	auth := &coreauth.Auth{ID: authID, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": "openai-upstream-key"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	router := newAzureOpenAITestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions?api-version=preview", strings.NewReader(`{"model":"`+alias+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("upstream path = %q, want /chat/completions", gotPath)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != alias {
		t.Fatalf("upstream model = %q, want %q; body=%s", got, alias, string(gotBody))
	}
}

func TestAzureOpenAIDeploymentChatCompletionsRoutesToClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		authID = "azure-ingress-claude-auth"
		alias  = "azure-ingress-claude-alias"
	)

	var gotPath string
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_azure_ingress_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"claude ok\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	auth := &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": "claude-upstream-key"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	router := newAzureOpenAITestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+alias+"/chat/completions?api-version=2024-10-21", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", gotPath)
	}
	if gotAuthorization != "Bearer claude-upstream-key" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != alias {
		t.Fatalf("upstream Claude model = %q, want %q; body=%s", got, alias, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content.0.text").String(); got != "hi" {
		t.Fatalf("upstream Claude content = %q, want hi; body=%s", got, string(gotBody))
	}
	if got := gjson.Get(resp.Body.String(), "choices.0.message.content").String(); got != "claude ok" {
		t.Fatalf("OpenAI response content = %q, want claude ok; body=%s", got, resp.Body.String())
	}
}

func TestAzureOpenAIV1ChatCompletionsRoutesToClaude(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		authID = "azure-ingress-claude-v1-auth"
		alias  = "azure-ingress-claude-v1-alias"
	)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_azure_ingress_2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"claude v1 ok\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
	auth := &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": "claude-upstream-key"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	router := newAzureOpenAITestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"`+alias+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("upstream path = %q, want /v1/messages", gotPath)
	}
	if got := gjson.Get(resp.Body.String(), "choices.0.message.content").String(); got != "claude v1 ok" {
		t.Fatalf("OpenAI response content = %q, want claude v1 ok; body=%s", got, resp.Body.String())
	}
}

func TestAzureOpenAIDeploymentChatCompletionsStreamsOpenAICompat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		provider = "azure-ingress-openai-stream"
		authID   = "azure-ingress-openai-stream-auth"
		alias    = "azure-ingress-openai-stream-alias"
	)

	var gotRawQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"stream ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, &config.Config{}))
	auth := &coreauth.Auth{ID: authID, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": upstream.URL, "api_key": "openai-upstream-key"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: alias, Type: "openai", UserDefined: true}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	router := newAzureOpenAITestRouter(manager)
	req := httptest.NewRequest(http.MethodPost, "/openai/deployments/"+alias+"/chat/completions?api-version=2024-10-21", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotRawQuery != "" {
		t.Fatalf("upstream query = %q, want empty", gotRawQuery)
	}
	if !strings.Contains(resp.Body.String(), "stream ok") {
		t.Fatalf("stream body missing content: %s", resp.Body.String())
	}
}

func newAzureOpenAITestRouter(manager *coreauth.Manager) *gin.Engine {
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewAzureOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/openai/deployments/:deployment/chat/completions", h.DeploymentChatCompletions)
	router.POST("/openai/v1/chat/completions", h.ChatCompletions)
	return router
}
