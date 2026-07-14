package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketRestoresPinnedAuthAfterFailedModelSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-good", "auth-bad-ws", "auth-bad-http"}}
	executor := &websocketDirectCaptureExecutor{done: make(chan struct{}), active: true, errorOnRequest: 2}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authGood := &coreauth.Auth{
		ID:         "auth-good",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	authBadWS := &coreauth.Auth{
		ID:         "auth-bad-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	authBadHTTP := &coreauth.Auth{
		ID:       "auth-bad-http",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	for _, auth := range []*coreauth.Auth{authGood, authBadWS, authBadHTTP} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register auth %s: %v", auth.ID, err)
		}
	}

	registry.GetGlobalRegistry().RegisterClient(authGood.ID, authGood.Provider, []*registry.ModelInfo{{ID: "good-model"}})
	registry.GetGlobalRegistry().RegisterClient(authBadWS.ID, authBadWS.Provider, []*registry.ModelInfo{{ID: "bad-model"}})
	registry.GetGlobalRegistry().RegisterClient(authBadHTTP.ID, authBadHTTP.Provider, []*registry.ModelInfo{{ID: "bad-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authGood.ID)
		registry.GetGlobalRegistry().UnregisterClient(authBadWS.ID)
		registry.GetGlobalRegistry().UnregisterClient(authBadHTTP.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	requests := []string{
		`{"type":"response.create","model":"good-model","input":[{"type":"message","id":"msg-1","role":"user","content":"first"}]}`,
		`{"type":"response.create","model":"bad-model","input":[{"type":"message","id":"msg-2","role":"user","content":"second"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-3","role":"user","content":"third"}]}`,
	}
	wantTypes := []string{wsEventTypeCompleted, wsEventTypeError, wsEventTypeCompleted}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		if errSet := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); errSet != nil {
			t.Fatalf("set read deadline %d: %v", i+1, errSet)
		}
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errRead)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wantTypes[i] {
			t.Fatalf("message %d type = %s, want %s: %s", i+1, got, wantTypes[i], payload)
		}
	}

	if got := executor.AuthIDs(); len(got) != 3 || got[0] != "auth-good" || got[1] != "auth-bad-ws" || got[2] != "auth-good" {
		t.Fatalf("selected auth IDs = %v, want [auth-good auth-bad-ws auth-good]", got)
	}
}
