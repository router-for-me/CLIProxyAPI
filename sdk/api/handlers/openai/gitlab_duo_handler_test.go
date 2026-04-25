package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/registry"
	runtimeexecutor "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/runtime/executor"
	_ "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/translator"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestOpenAIChatCompletionsWithGitLabDuoOpenAIGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotAuthHeader, gotRealmHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from duo openai\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from duo openai\"}]}],\"usage\":{\"input_tokens\":11,\"output_tokens\":4,\"total_tokens\":15}}}\n\n"))
	}))
	defer upstream.Close()

	manager := registerGitLabDuoOpenAIAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5-codex",
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/openai/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/openai/v1/responses")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("x-gitlab-realm = %q, want saas", gotRealmHeader)
	}
	if !strings.Contains(resp.Body.String(), `"content":"hello from duo openai"`) {
		t.Fatalf("expected translated chat completion, got %s", resp.Body.String())
	}
}

func TestOpenAIResponsesStreamWithGitLabDuoOpenAIGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotAuthHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"streamed duo output\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"created_at\":1710000000,\"model\":\"gpt-5-codex\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"streamed duo output\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":3,\"total_tokens\":13}}}\n\n"))
	}))
	defer upstream.Close()

	manager := registerGitLabDuoOpenAIAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5-codex",
		"stream":true,
		"input":"hello"
	}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/openai/v1/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/openai/v1/responses")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if !strings.Contains(resp.Body.String(), `"type":"response.output_text.delta"`) {
		t.Fatalf("expected streamed responses delta, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"type":"response.completed"`) {
		t.Fatalf("expected streamed responses completion, got %s", resp.Body.String())
	}
}

func registerGitLabDuoOpenAIAuth(t *testing.T, upstreamURL string) *coreauth.Manager {
	t.Helper()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewGitLabExecutor(&internalconfig.Config{}))

	auth := &coreauth.Auth{
		ID:       "gitlab-duo-openai-handler-test",
		Provider: "gitlab",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"duo_gateway_base_url": upstreamURL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "openai",
			"model_name":           "gpt-5-codex",
		},
	}
	registered, err := manager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(registered.ID, registered.Provider, runtimeexecutor.GitLabModelsFromAuth(registered))
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(registered.ID)
	})
	return manager
}
