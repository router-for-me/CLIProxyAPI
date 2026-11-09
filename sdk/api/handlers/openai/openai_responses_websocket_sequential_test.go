package openai

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestResponsesWebsocketLeavesAccountSequentialRoutingMovedFrom(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelName := "sequential-websocket-model"
	executor := &websocketDirectCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, coreauth.NewSequentialSelector(5, ""), nil)
	manager.RegisterExecutor(executor)
	for _, authID := range []string{"sequential-ws-a", "sequential-ws-b"} {
		auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", authID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelName}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	setUsage := func(used string) {
		t.Helper()
		auth, ok := manager.GetByID("sequential-ws-a")
		if !ok {
			t.Fatal("auth a missing")
		}
		auth.Quota.Signals = map[string]string{
			"X-Codex-Primary-Used-Percent": used,
			"X-Codex-Primary-Reset-At":     strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
		}
		if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", nil)
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()
	send := func(turn int) {
		t.Helper()
		request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[{"type":"message","id":"msg-%d"}]}`, modelName, turn)
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
			t.Fatalf("write turn %d: %v", turn, errWrite)
		}
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Fatalf("read turn %d: %v", turn, errRead)
		}
	}

	send(1)
	setUsage("50")
	send(2)
	setUsage("96")
	send(3)

	got := executor.AuthIDs()
	if len(got) != 3 || got[0] != "sequential-ws-a" || got[1] != "sequential-ws-a" || got[2] != "sequential-ws-b" {
		t.Fatalf("selected auth IDs = %v, want [a a b]", got)
	}
}
