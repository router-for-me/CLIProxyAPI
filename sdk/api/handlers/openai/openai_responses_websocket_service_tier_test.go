package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

// Switching a pinned session to ultrafast with a full create moves it to an
// eligible credential instead of failing on, or downgrading at, the pinned one.
func TestResponsesWebsocketUltrafastCreateLeavesIneligiblePinnedAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &websocketDirectCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "ultrafast-ws-model"
	for _, auth := range []*coreauth.Auth{
		{ID: "ws-pro", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true", "plan_type": "pro", "priority": "10"}, Metadata: map[string]any{"access_token": "pro-token"}},
		{ID: "ws-promax", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true", "plan_type": "promax"}, Metadata: map[string]any{"access_token": "promax-token"}},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	for i, tier := range []string{"priority", "ultrafast", "ultrafast"} {
		request := `{"type":"response.create","model":"` + model + `","service_tier":"` + tier + `","input":[{"type":"message","id":"msg"}]}`
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Fatalf("write %d: %v", i+1, errWrite)
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read %d: %v", i+1, errRead)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("turn %d (%s) = %s, want %s", i+1, tier, payload, wsEventTypeCompleted)
		}
	}
	if got := executor.AuthIDs(); len(got) != 3 || got[0] != "ws-pro" || got[1] != "ws-promax" || got[2] != "ws-promax" {
		t.Fatalf("selected auths = %v, want [ws-pro ws-promax ws-promax]", got)
	}
}
