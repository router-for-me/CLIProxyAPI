package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketMetadataExecutor struct {
	websocketCaptureExecutor
}

func (*websocketMetadataExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 5)
	for _, payload := range []string{
		`data: {"type":"response.created","response":{"id":"resp-1"}}`,
		`data: {"type":"codex.response.metadata","headers":{"x-turn-state":"turn-1"}}`,
		`data: {"type":"response.metadata","metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{}}}}`,
		`data: {"type":"response.subscription_usage","subscription":{"tier":"private"}}`,
		`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`,
	} {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func TestResponsesWebsocketFiltersModerationMetadataForStandardClients(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		headers   http.Header
		wantTypes []string
	}{
		{
			name:      "standard client",
			headers:   http.Header{"User-Agent": []string{"standard-client/1.0"}},
			wantTypes: []string{"response.created", "codex.response.metadata", "response.completed"},
		},
		{
			name:      "codex user agent",
			headers:   http.Header{"User-Agent": []string{"codex_cli_rs/1.0"}},
			wantTypes: []string{"response.created", "codex.response.metadata", "response.metadata", "response.completed"},
		},
		{
			name:      "codex originator",
			headers:   http.Header{"Originator": []string{"codex_cli_rs"}},
			wantTypes: []string{"response.created", "codex.response.metadata", "response.metadata", "response.completed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &websocketMetadataExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{ID: "websocket-metadata-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "websocket-metadata-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.GET("/v1/responses", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
			conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, tc.headers)
			if errDial != nil {
				t.Fatalf("dial websocket: %v", errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()
			request := `{"type":"response.create","model":"websocket-metadata-model","input":[]}`
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
				t.Fatalf("write request: %v", errWrite)
			}

			var gotTypes []string
			for {
				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatalf("read websocket: %v; events: %v", errRead, gotTypes)
				}
				eventType := gjson.GetBytes(payload, "type").String()
				gotTypes = append(gotTypes, eventType)
				if eventType == "error" {
					t.Fatalf("unexpected error event: %s", payload)
				}
				if eventType == "response.completed" {
					break
				}
			}
			if !slices.Equal(gotTypes, tc.wantTypes) {
				t.Fatalf("event types = %v, want %v", gotTypes, tc.wantTypes)
			}
		})
	}
}
