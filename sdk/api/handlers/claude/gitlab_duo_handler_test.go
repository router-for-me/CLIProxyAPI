package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestClaudeMessagesWithGitLabDuoAnthropicGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath, gotAuthHeader, gotRealmHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthHeader = r.Header.Get("Authorization")
		gotRealmHeader = r.Header.Get("X-Gitlab-Realm")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"cmd":"ls"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":4}}`))
	}))
	defer upstream.Close()

	manager, _ := registerGitLabDuoAnthropicAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"max_tokens":128,
		"messages":[{"role":"user","content":"list files"}],
		"tools":[{"name":"Bash","description":"run bash","input_schema":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if gotAuthHeader != "Bearer gateway-token" {
		t.Fatalf("authorization = %q, want Bearer gateway-token", gotAuthHeader)
	}
	if gotRealmHeader != "saas" {
		t.Fatalf("x-gitlab-realm = %q, want saas", gotRealmHeader)
	}
	if !strings.Contains(resp.Body.String(), `"tool_use"`) {
		t.Fatalf("expected tool_use response, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"Bash"`) {
		t.Fatalf("expected Bash tool in response, got %s", resp.Body.String())
	}
}

func TestClaudeMessagesStreamWithGitLabDuoAnthropicGateway(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello from duo\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":10,\"output_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer upstream.Close()

	manager, _ := registerGitLabDuoAnthropicAuth(t, upstream.URL)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{
		"model":"claude-sonnet-4-5",
		"stream":true,
		"max_tokens":64,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if gotPath != "/v1/proxy/anthropic/v1/messages" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/proxy/anthropic/v1/messages")
	}
	if got := resp.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", got)
	}
	if !strings.Contains(resp.Body.String(), "event: content_block_delta") {
		t.Fatalf("expected streamed claude event, got %s", resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "hello from duo") {
		t.Fatalf("expected streamed text, got %s", resp.Body.String())
	}
}

func registerGitLabDuoAnthropicAuth(t *testing.T, upstreamURL string) (*coreauth.Manager, string) {
	t.Helper()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(runtimeexecutor.NewGitLabExecutor(&internalconfig.Config{}))

	auth := &coreauth.Auth{
		ID:       "gitlab-duo-claude-handler-test",
		Provider: "gitlab",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"duo_gateway_base_url": upstreamURL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]string{"X-Gitlab-Realm": "saas"},
			"model_provider":       "anthropic",
			"model_name":           "claude-sonnet-4-5",
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
	return manager, registered.ID
}
